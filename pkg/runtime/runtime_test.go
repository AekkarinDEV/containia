package runtime

import (
	"strings"
	"testing"
)

func TestContainerEnvironmentDoesNotInheritHostLocale(t *testing.T) {
	t.Setenv("LC_ALL", "missing_HOST_locale")
	t.Setenv("LC_TIME", "th_TH.UTF-8")
	t.Setenv("LANG", "host_LANG")
	t.Setenv("CONTAINIA_TEST_HOST_SECRET", "host-only")
	env := containerEnvironment([]string{"LANG=en_US.utf8", "PATH=/image/bin", "VALUE=image", "VALUE=user=override"})
	values := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if _, duplicate := values[key]; duplicate {
			t.Fatalf("duplicate environment key %s", key)
		}
		values[key] = value
	}
	for _, key := range []string{"LC_ALL", "LC_TIME", "CONTAINIA_TEST_HOST_SECRET"} {
		if _, exists := values[key]; exists {
			t.Errorf("host setting %s leaked into container", key)
		}
	}
	for key, want := range map[string]string{"LANG": "en_US.utf8", "PATH": "/image/bin", "VALUE": "user=override"} {
		if values[key] != want {
			t.Errorf("%s = %q, want %q", key, values[key], want)
		}
	}
}

func TestContainerEnvironmentAllowsExplicitLocaleOverride(t *testing.T) {
	env := strings.Join(containerEnvironment([]string{"LC_ALL=C.UTF-8"}), "\n")
	if !strings.Contains(env, "LC_ALL=C.UTF-8") {
		t.Fatal("explicit locale override was lost")
	}
}
