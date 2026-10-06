package registry

import "testing"

func TestParseEvents(t *testing.T) {
	body := []byte(`{"events":[
	 {"id":"1","timestamp":"2026-10-06T10:00:00Z","action":"push","target":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:a","repository":"shop/api","tag":"v1"},"request":{"addr":"10.90.0.2:4312","method":"PUT","useragent":"docker/27"},"actor":{"name":"u1"}},
	 {"id":"2","action":"push","target":{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:l","repository":"shop/api"},"request":{"method":"PUT"}},
	 {"id":"3","action":"pull","target":{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:i","repository":"shop/api","tag":"v1"},"request":{"addr":"[::1]:9","method":"GET"},"actor":{"name":"node"}},
	 {"id":"4","action":"pull","target":{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:i","repository":"shop/api"},"request":{"method":"HEAD"}},
	 {"id":"5","action":"pull","target":{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:i","repository":"shop/api"},"request":{"method":"GET"},"actor":{"name":"syncloud-controller"}},
	 {"id":"6","action":"delete","target":{"digest":"sha256:a","repository":"shop/api"}},
	 {"id":"7","action":"mount","target":{"repository":"shop/api"}}
	]}`)
	evs, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 {
		t.Fatalf("got %d events: %+v", len(evs), evs)
	}
	if evs[0].Action != "push" || evs[0].Addr != "10.90.0.2" || evs[0].Tag != "v1" || evs[0].Actor != "u1" {
		t.Errorf("push: %+v", evs[0])
	}
	if evs[1].Action != "pull" || evs[1].Addr != "::1" || evs[1].Digest != "sha256:i" {
		t.Errorf("pull: %+v", evs[1])
	}
	if evs[3].Action != "delete" || evs[3].At.IsZero() {
		t.Errorf("delete: %+v", evs[3])
	}
}
