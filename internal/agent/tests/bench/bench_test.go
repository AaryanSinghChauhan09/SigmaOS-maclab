package bench

import "testing"

func TestTheManifestPinsEveryItem(t *testing.T) {
	items, err := Manifest()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"speedometer", "motionmark", "jetstream", "chromium", "chrome", "7zip", "glmark2", "vkmark",
		"assimp", "clpeak", "vkpeak", "geekbench", "kernel", "clip"}
	have := map[string]Item{}
	for _, it := range items {
		have[it.Name] = it
	}
	for _, n := range want {
		if _, ok := have[n]; !ok {
			t.Errorf("no %s in benchmarks.json", n)
		}
	}
	if have["speedometer"].Version != "3.1" || have["motionmark"].Version != "1.3" || have["jetstream"].Version != "2.2" {
		t.Errorf("browser benchmark versions: %+v", have)
	}
}

func TestAManifestWithoutAShaIsRefused(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"items":[{"name":"x","version":"1","url":"https://x","sha256":"abc"}]}`)); err == nil {
		t.Fatal("accepted")
	}
}
