// A Mac's page while it is open. The lab's event stream says when the Mac
// has changed since the page was made; on Live it also brings each new
// capture of the screen and each kernel event, and the page reads the Mac's
// logs. A page that is view only asks the Mac for no capture.
(function () {
  "use strict";
  var root = document.querySelector("[data-mac]");
  if (!root) return;
  var data = root.dataset;
  var live = "live" in data;
  var changed = document.getElementById("mac-changed");

  // The same line the server made the page from (see macChanges).
  function changes(d) {
    var l = d.lease;
    return [d.state, d.active_job || "", l ? l.account_id || 0 : 0, l ? Math.floor(Date.parse(l.expires) / 1000) : 0].join("|");
  }

  function parsed(event) {
    try { return JSON.parse(event.data); } catch (e) { return null; }
  }

  function stale() {
    if (changed) changed.hidden = false;
  }

  var img = root.querySelector("[data-screen]");
  var note = root.querySelector("[data-screen-note]");
  var capturing = false;

  function say(text) {
    if (note) note.textContent = text;
  }

  // A time as the page's records say it: in Brisbane, which keeps one offset.
  function when(time) {
    var t = new Date(time).getTime();
    if (isNaN(t)) return "";
    return new Date(t + 10 * 3600 * 1000).toISOString().slice(0, 16).replace("T", " ") + " AEST";
  }

  function showScreen(name, time) {
    if (!img || !name) return;
    img.src = data.api + "/screens/" + encodeURIComponent(name);
    img.hidden = false;
    say("Latest capture: " + when(time) + ".");
  }

  function capture() {
    if (capturing) return;
    capturing = true;
    fetch(data.api + "/screenshot", { method: "POST", headers: { "X-CSRF-Token": data.csrf } })
      .then(function (r) {
        return r.ok ? r.json().then(function (shot) { showScreen(shot.name, shot.time); })
          : r.text().then(function (why) { stopAuto(); say("No capture: " + why.trim()); });
      })
      .catch(function () { stopAuto(); say("No capture: the lab did not answer."); })
      .then(function () { capturing = false; });
  }

  var timer = null;

  function setAuto(seconds) {
    clearInterval(timer);
    timer = seconds > 0 ? setInterval(function () { if (!document.hidden) capture(); }, seconds * 1000) : null;
    root.querySelectorAll("[data-auto]").forEach(function (chip) {
      chip.classList.toggle("current", Number(chip.dataset.auto) === seconds);
    });
  }

  function stopAuto() { setAuto(0); }

  var log = root.querySelector("[data-log]");

  function readLog() {
    if (!log) return;
    fetch(data.api + "/logs?source=" + encodeURIComponent(data.logs) + "&lines=500")
      .then(function (r) { return r.text(); })
      .then(function (text) { log.textContent = text; log.scrollTop = log.scrollHeight; })
      .catch(function () { log.textContent = "The lab did not answer."; });
  }

  if (live) {
    root.addEventListener("click", function (event) {
      var el = event.target.closest ? event.target.closest("button") : null;
      if (!el) return;
      if ("capture" in el.dataset) capture();
      if ("auto" in el.dataset) setAuto(Number(el.dataset.auto));
      if ("refresh" in el.dataset) readLog();
    });
    if (!("notNow" in data)) readLog();
  }

  if (!window.EventSource) return;
  var stream = new EventSource(data.stream);

  stream.addEventListener("device", function (event) {
    var d = parsed(event);
    if (!d || d.name !== data.mac || changes(d) === data.changes) return;
    // On Live, whose Lease it is decides what may be seen at all.
    if (data.tab === "live" && changes(d).split("|")[2] !== data.changes.split("|")[2]) {
      location.reload();
      return;
    }
    stale();
  });

  stream.addEventListener("job", function () {
    if (data.tab === "overview") stale();
  });

  if (!live) return;

  stream.addEventListener("screen", function (event) {
    var shot = parsed(event);
    if (shot && !shot.job) showScreen(shot.name, shot.time);
  });

  var events = root.querySelector("[data-events]");
  stream.addEventListener("kevent", function (event) {
    var e = parsed(event);
    if (!events || !e) return;
    var li = document.createElement("li");
    var at = document.createElement("span");
    at.className = "when";
    at.textContent = when(e.time);
    li.appendChild(at);
    li.appendChild(document.createTextNode(" " + e.kind + ": " + e.line));
    events.insertBefore(li, events.firstChild);
  });
})();
