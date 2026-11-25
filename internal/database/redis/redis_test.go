package redis

import "testing"

func TestClusterAndSentinelParsing(t *testing.T) {
	c, handled, err := parseClusterOrSentinel("redis+cluster://user:pass@10.0.0.1:6379,10.0.0.2:6379", -1, nil)
	if !handled || err != nil || c.mode != "cluster" {
		t.Fatalf("cluster parse = (handled=%v, err=%v, c=%+v)", handled, err, c)
	}
	_ = c.Close()

	s, handled, err := parseClusterOrSentinel("redis+sentinel://10.0.0.1:26379,10.0.0.2?master_name=prod-master", 2, nil)
	if !handled || err != nil || s.mode != "sentinel" || s.db != 2 {
		t.Fatalf("sentinel parse = (handled=%v, err=%v, c=%+v)", handled, err, s)
	}
	_ = s.Close()

	_, handled, _ = parseClusterOrSentinel("redis://localhost:6379/0", 0, nil)
	if handled {
		t.Fatal("standalone redis:// should not be handled by cluster/sentinel parser")
	}
}
