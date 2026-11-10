package config

import "testing"

func TestMatchNameBlacklist(t *testing.T) {
	cfg := Config{NameBlacklist: []string{"看我主页", "Ad Bot", " ", "--"}}
	for _, kw := range cfg.NameBlacklist {
		if n := normalizeName(kw); n != "" {
			cfg.blacklist = append(cfg.blacklist, blacklistKeyword{raw: kw, normalized: n})
		}
	}

	cases := []struct {
		names []string
		want  string
		hit   bool
	}{
		{[]string{"没路子", "看我主页"}, "看我主页", true},
		{[]string{"看 我 主 页"}, "看我主页", true},
		{[]string{"看-我·主|页!!"}, "看我主页", true},
		{[]string{"小明", "", "ADBOT_123"}, "Ad Bot", true},
		{[]string{"看我", "主页"}, "", false},
		{[]string{"正常用户", "", "normal_user"}, "", false},
	}
	for _, c := range cases {
		got, hit := cfg.MatchNameBlacklist(c.names...)
		if hit != c.hit || got != c.want {
			t.Errorf("MatchNameBlacklist(%q) = %q, %v; want %q, %v", c.names, got, hit, c.want, c.hit)
		}
	}
}

func TestMatchNameBlacklistEmpty(t *testing.T) {
	var cfg Config
	if _, hit := cfg.MatchNameBlacklist("看我主页"); hit {
		t.Error("空黑名单不应命中")
	}
}
