package apps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstalledParsesPackagesList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packages.list")
	data := `
# package uid debuggable dataDir ...
org.telegram.messenger 10123 0 /data/user/0/org.telegram.messenger default 35
com.android.settings 1000 0 /system/priv-app/Settings default 35
broken.uid nope 0 /data/user/0/broken default 35
too_short
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	apps, err := LoadInstalled(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 {
		t.Fatalf("expected two parsed apps, got %#v", apps)
	}
	if apps[0].PackageName != "org.telegram.messenger" || apps[0].AppName != "Messenger" || apps[0].Category != "MESSAGING" || apps[0].IsSystemApp {
		t.Fatalf("unexpected user app: %#v", apps[0])
	}
	if apps[1].PackageName != "com.android.settings" || apps[1].Category != "SYSTEM" || !apps[1].IsSystemApp {
		t.Fatalf("unexpected system app: %#v", apps[1])
	}
}

func TestResolveUIDUsesExactMatchBeforeAndroidUserFallback(t *testing.T) {
	apps := []Info{
		{PackageName: "secondary.user", UID: 1010123},
		{PackageName: "primary.user", UID: 10123},
	}

	app, ok := ResolveUID(apps, 10123)
	if !ok || app.PackageName != "primary.user" {
		t.Fatalf("expected exact UID match, got %#v ok=%v", app, ok)
	}

	app, ok = ResolveUID(apps, 2010123)
	if !ok || app.PackageName != "secondary.user" {
		t.Fatalf("expected Android user fallback match, got %#v ok=%v", app, ok)
	}
}

func TestPrettyPackageLabelAndClassification(t *testing.T) {
	if got := PrettyPackageLabel("com.example.my-app_name"); got != "My app name" {
		t.Fatalf("unexpected label: %q", got)
	}
	cases := map[string]string{
		"com.spotify.music":      "AUDIO",
		"org.mozilla.firefox":    "BROWSER",
		"com.example.game":       "GAME",
		"ru.sberbankmobile":      "PRODUCTIVITY",
		"com.instagram.android":  "SOCIAL",
		"com.example.unclassified": "OTHER",
	}
	for packageName, want := range cases {
		if got := Classify(packageName, false); got != want {
			t.Fatalf("Classify(%q) = %q, want %q", packageName, got, want)
		}
	}
}
