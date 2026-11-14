package githubtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func fake(t *testing.T) (*Server, string) {
	t.Helper()
	g := New("the-client-id", "the-client-secret")
	g.AddUser(1001, "ada")
	hs := httptest.NewServer(g)
	t.Cleanup(hs.Close)
	return g, hs.URL
}

func postForm(t *testing.T, target string, form url.Values) (int, map[string]any) {
	t.Helper()
	res, err := http.PostForm(target, form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

func appToken(t *testing.T, base, method, id, secret, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, base+"/applications/"+id+"/token", strings.NewReader(`{"access_token":"`+token+`"}`))
	req.SetBasicAuth(id, secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

// The device flow as omarchy-m-test runs it: a code to type, pending until a
// person approves it at /login/device, then a token, once. The app then
// learns whose the token is with its own id and secret, and revokes it.
func TestTheDeviceFlowGivesATokenTheAppCanNameAndRevoke(t *testing.T) {
	g, base := fake(t)

	if status, _ := postForm(t, base+"/login/device/code", url.Values{"client_id": {"another-app"}, "scope": {""}}); status != http.StatusNotFound {
		t.Errorf("a device code for another app's client id: %d", status)
	}
	status, device := postForm(t, base+"/login/device/code", url.Values{"client_id": {g.ClientID}, "scope": {""}})
	if status != http.StatusOK || device["verification_uri"] != base+"/login/device" || device["interval"] != float64(5) {
		t.Fatalf("the device code: %d %v", status, device)
	}
	userCode, deviceCode := device["user_code"].(string), device["device_code"].(string)
	if len(userCode) != 9 || userCode[4] != '-' || len(deviceCode) < 20 {
		t.Fatalf("codes %q %q", userCode, deviceCode)
	}
	poll := url.Values{"client_id": {g.ClientID}, "device_code": {deviceCode}, "grant_type": {DeviceGrant}}
	if _, body := postForm(t, base+"/login/oauth/access_token", poll); body["error"] != "authorization_pending" {
		t.Fatalf("a poll before anyone approved: %v", body)
	}
	if _, body := postForm(t, base+"/login/oauth/access_token", url.Values{"client_id": {g.ClientID}, "device_code": {"nope"}, "grant_type": {DeviceGrant}}); body["error"] != "incorrect_device_code" {
		t.Errorf("a poll with a code nobody was given: %v", body)
	}

	page, err := http.Get(base + "/login/device?user_code=" + userCode)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !strings.Contains(string(text), "This is not GitHub") || !strings.Contains(string(text), "@ada (account id 1001)") {
		t.Errorf("the page a person types the code at:\n%s", text)
	}
	if res, err := http.Get(base + "/login/device?user_code=" + strings.ToLower(userCode) + "&as=1001"); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("approving: %v", err)
	} else {
		res.Body.Close()
	}
	if g.Approve(userCode, 1001) {
		t.Error("a code was approved twice")
	}

	_, body := postForm(t, base+"/login/oauth/access_token", poll)
	token, _ := body["access_token"].(string)
	if !strings.HasPrefix(token, "gho_") {
		t.Fatalf("the poll after the approval: %v", body)
	}
	if _, body := postForm(t, base+"/login/oauth/access_token", poll); body["error"] != "incorrect_device_code" {
		t.Errorf("the device code gave a token twice: %v", body)
	}

	if status, _ := appToken(t, base, "POST", g.ClientID, "a-wrong-secret", token); status != http.StatusUnauthorized {
		t.Errorf("the token's check without the app's secret: %d", status)
	}
	status, named := appToken(t, base, "POST", g.ClientID, g.ClientSecret, token)
	if user, _ := named["user"].(map[string]any); status != http.StatusOK || user["login"] != "ada" || user["id"] != float64(1001) {
		t.Fatalf("whose the token is: %d %v", status, named)
	}
	g.IssueElsewhere("gho_fromAnotherApp", 1001)
	if status, _ := appToken(t, base, "POST", g.ClientID, g.ClientSecret, "gho_fromAnotherApp"); status != http.StatusNotFound {
		t.Errorf("a token another app issued: %d", status)
	}
	if status, _ := appToken(t, base, "DELETE", g.ClientID, g.ClientSecret, token); status != http.StatusNoContent {
		t.Errorf("revoking: %d", status)
	}
	if status, _ := appToken(t, base, "POST", g.ClientID, g.ClientSecret, token); status != http.StatusNotFound {
		t.Errorf("a revoked token still names its account: %d", status)
	}
	if got := g.Revoked(); len(got) != 1 || got[0] != token {
		t.Errorf("revoked %q", got)
	}
}

// Signed in already, as SignInAs has it, a device code is approved as it is
// asked for.
func TestSignInAsApprovesADeviceCodeAtOnce(t *testing.T) {
	g, base := fake(t)
	g.SignInAs(1001)
	_, device := postForm(t, base+"/login/device/code", url.Values{"client_id": {g.ClientID}})
	_, body := postForm(t, base+"/login/oauth/access_token", url.Values{"client_id": {g.ClientID}, "device_code": {device["device_code"].(string)}, "grant_type": {DeviceGrant}})
	if token, _ := body["access_token"].(string); token == "" {
		t.Fatalf("the first poll: %v", body)
	}
}
