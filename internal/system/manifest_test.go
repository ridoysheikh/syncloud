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

func TestRegistryStorage(t *testing.T) {
	find := func(c Config, id string) map[string]string {
		for _, s := range Specs(c) {
			if s.TaskId == id {
				return s.Env
			}
		}
		return nil
	}
	if e := find(Config{}, "sys-registry"); e["REGISTRY_STORAGE"] != "" {
		t.Fatalf("disk registry has storage env %v", e)
	}
	if find(Config{}, RegistryCopyTaskID) != nil {
		t.Fatal("the copy registry runs without a switch")
	}
	s3 := &RegistryS3{URL: "https://s3.example.com", AccessKeyID: "a", SecretAccessKey: "s", Bucket: "b", Prefix: "reg/x"}
	e := find(Config{RegistryS3: s3, RegistryReadOnly: true}, "sys-registry")
	if e["REGISTRY_STORAGE"] != "s3" || e["REGISTRY_STORAGE_S3_REGION"] != "us-east-1" || e["REGISTRY_STORAGE_S3_SECURE"] != "true" ||
		e["REGISTRY_STORAGE_S3_ROOTDIRECTORY"] != "/reg/x" || e["REGISTRY_STORAGE_S3_BUCKET"] != "b" ||
		e["REGISTRY_STORAGE_MAINTENANCE_READONLY"] == "" || e["REGISTRY_NOTIFICATIONS_ENDPOINTS"] == "" {
		t.Fatalf("s3 registry env %v", e)
	}
	c := find(Config{RegistryCopy: &RegistryCopy{S3: s3}}, RegistryCopyTaskID)
	if c["REGISTRY_HTTP_ADDR"] != RegistryCopyAddr || c["REGISTRY_STORAGE"] != "s3" || c["REGISTRY_NOTIFICATIONS_ENDPOINTS"] != "" {
		t.Fatalf("copy registry env %v", c)
	}
}
