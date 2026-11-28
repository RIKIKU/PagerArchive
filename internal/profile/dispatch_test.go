package profile

import (
	"os"
	"strings"
	"testing"
)

// withDispatch is the example network profile with its zones layer's
// dispatch_channels replaced by keys (JSON members, without braces).
func withDispatch(t *testing.T, keys string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../profiles/example/example-network.json")
	if err != nil {
		t.Fatal(err)
	}
	const was = `"dispatch_channels": { "A": 501, "B": 502 }`
	if !strings.Contains(string(b), was) {
		t.Fatal("the example's zones have no dispatch_channels")
	}
	return []byte(strings.Replace(string(b), was, keys, 1))
}

// A160 (R92f): a layer's dispatch channels, by template and by name, read
// as R92a reads a number.
func TestA160DispatchChannels(t *testing.T) {
	n, errs := loadNetwork("example-network.json", withDispatch(t, `"dispatch_channel": "5{name:02}", "dispatch_channels": { "Central": 701, "08": 999 }`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	d := n.Areas[0].Dispatch
	for name, want := range map[string]int32{"Central": 701, "08": 999, "13": 513, "8": 508, "0": 500, "007": 507} {
		if got, ok := d.Channel(name); !ok || got != want {
			t.Errorf("%q: %d %v, want %d", name, got, ok, want)
		}
	}
	for _, name := range []string{"North", "", "8A", " 8", string(rune(0x0663))} {
		if got, ok := d.Channel(name); ok {
			t.Errorf("%q: %d, want none", name, got)
		}
	}
	if n.Areas[1].Dispatch != nil {
		t.Errorf("regions name none, but have %+v", n.Areas[1].Dispatch)
	}
	// The example: by name only.
	ex, errs := loadNetwork("example-network.json", withDispatch(t, `"dispatch_channels": { "A": 501, "B": 502 }`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if a, _ := ex.Areas[0].Dispatch.Channel("A"); a != 501 {
		t.Errorf("example zone A: %d", a)
	}
	if _, ok := ex.Areas[0].Dispatch.Channel("C"); ok {
		t.Error("example zone C, which isn't listed, has one")
	}
	// No width, and a suffix; and too many digits.
	plain := &DispatchChannels{Template: true}
	if got, ok := plain.Channel("007"); !ok || got != 7 {
		t.Errorf(`"{name}" of "007": %d %v`, got, ok)
	}
	if _, ok := plain.Channel("1234567890"); ok {
		t.Error("10 digits gave a channel")
	}
	if got, ok := plain.Channel("0001234567"); !ok || got != 1234567 {
		t.Errorf("leading zeros then 7 digits: %d %v", got, ok)
	}
	suffix := &DispatchChannels{Template: true, Prefix: "0", Suffix: "1", Width: 3}
	if got, ok := suffix.Channel("8"); !ok || got != 81 {
		t.Errorf(`"0{name:03}1" of "8": %d %v, want 81 (0 008 1)`, got, ok)
	}
	var none *DispatchChannels
	if _, ok := none.Channel("08"); ok {
		t.Error("a layer with none gave one")
	}
}

// A160: a template or number that can't be is a profile error, naming
// where; changing them leaves the parse hash alone.
func TestA160DispatchChannelErrors(t *testing.T) {
	for keys, want := range map[string]string{
		`"dispatch_channel": "508"`:                "/areas/0/dispatch_channel",
		`"dispatch_channel": "5{name}{name}"`:      "/areas/0/dispatch_channel",
		`"dispatch_channel": "D{name}"`:            "/areas/0/dispatch_channel",
		`"dispatch_channel": "5{name:00}"`:         "/areas/0/dispatch_channel",
		`"dispatch_channel": "5{zone}"`:            "/areas/0/dispatch_channel",
		`"dispatch_channels": { "A": -1 }`:         "/areas/0/dispatch_channels",
		`"dispatch_channels": { "A": 1000000000 }`: "/areas/0/dispatch_channels",
		`"dispatch_channels": { "A": "501" }`:      "/areas/0/dispatch_channels",
	} {
		_, errs := loadNetwork("example-network.json", withDispatch(t, keys))
		if len(errs) == 0 {
			t.Errorf("%s: accepted", keys)
			continue
		}
		var all []string
		for _, e := range errs {
			all = append(all, e.Error())
		}
		if !strings.Contains(strings.Join(all, "; "), want) {
			t.Errorf("%s: %v doesn't name %s", keys, all, want)
		}
	}
	base, errs := loadNetwork("example-network.json", withDispatch(t, `"dispatch_channels": { "A": 501, "B": 502 }`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	for _, keys := range []string{`"dispatch_channel": "5{name:02}"`, `"dispatch_channels": { "A": 601 }`, `"_dispatch": "none yet"`} {
		n, errs := loadNetwork("example-network.json", withDispatch(t, keys))
		if len(errs) > 0 {
			t.Fatal(keys, errs)
		}
		if n.Hash != base.Hash {
			t.Errorf("%s: the parse hash changed", keys)
		}
	}
}
