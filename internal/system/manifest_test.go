package system

import "testing"

func TestGitServerIsOptional(t *testing.T) {
	for _, s := range Specs(Config{}) {
		if s.TaskId == GitServerTaskID {
			t.Fatal("the Git server runs while off")
		}
	}
	var git bool
	for _, s := range Specs(Config{GitServer: &GitServerConfig{RootURL: "https://git.example.com/", SecretKey: "k", InternalToken: "t"}}) {
		if s.TaskId != GitServerTaskID {
			continue
		}
		git = true
		e := s.Env
		if e["GITEA__server__ROOT_URL"] != "https://git.example.com/" || e["GITEA__server__DOMAIN"] != "git.example.com" ||
			e["GITEA__server__HTTP_ADDR"] != "127.0.0.1" || e["GITEA__server__HTTP_PORT"] != "3002" ||
			e["GITEA__service__DISABLE_REGISTRATION"] != "true" || e["GITEA__security__SECRET_KEY"] != "k" {
			t.Fatalf("env %v", e)
		}
		if s.NetworkMode != "host" || len(s.Mounts) != 1 || s.Mounts[0].Target != "/var/lib/gitea" {
			t.Fatalf("spec %+v", s)
		}
	}
	if !git {
		t.Fatal("the Git server is missing while on")
	}
}
