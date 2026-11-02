package sddownload

import (
	"strings"
	"testing"
)

func TestParseRequest(t *testing.T) {
	for _, body := range []string{
		`{"date":"20260228","hour":9,"minute":12}`,
		`{"date":"20260228","hour":"09","minute":"12","new":true}`,
	} {
		r, err := ParseRequest(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		path, name, err := r.paths()
		if err != nil || !strings.HasPrefix(path, "/media/mmc/record/20260228/09/12") || !strings.HasPrefix(name, "20260228-0912") {
			t.Fatalf("paths %q %q %v", path, name, err)
		}
		if r.New && !strings.HasSuffix(path, "12_new.mp4") {
			t.Fatal(path)
		}
	}
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"date":"20260228","hour":9,"minute":12} {}`,
		`{"date":"20260228","hour":9,"hour":10,"minute":12}`,
		`{"date":"20260229","hour":9,"minute":12}`,
		`{"date":"../20260","hour":9,"minute":12}`,
		`{"date":"20260228","hour":24,"minute":12}`,
		`{"date":"20260228","hour":9,"minute":60}`,
		`{"date":"20260228","hour":true,"minute":12}`,
		`{"date":"20260228","hour":null,"minute":12}`,
		`{"date":"20260228","hour":9.0,"minute":12}`,
		`{"date":"20260228","hour":9,"minute":12,"new":null}`,
		`{"date":"20260228","hour":9,"minute":12,"path":"/etc/passwd"}`,
		`{"Date":"20260228","hour":9,"minute":12}`,
		strings.Repeat(" ", 1025),
	} {
		if _, err := ParseRequest(strings.NewReader(body)); err != ErrRequest {
			t.Errorf("accepted invalid request %q: %v", body, err)
		}
	}
}

func TestPairingValidation(t *testing.T) {
	p := Pairing{IP: "192.168.2.3", UID: "ABCDEFGHIJKLMNOPQRST", ENR: strings.Repeat("x", 16), MAC: "aa:bb:cc:dd:ee:ff", Model: "WYZE_CAKP2JFUS", DTLS: true}
	n, err := p.normalized()
	if err != nil || n.MAC != "AABBCCDDEEFF" {
		t.Fatalf("normalize %v %v", n.MAC, err)
	}
	for _, ip := range []string{"8.8.8.8", "127.0.0.1", "169.254.1.1", "0.0.0.0", "::1", "::ffff:192.168.2.3", "192.168.2.3:80"} {
		bad := p
		bad.IP = ip
		if _, err := bad.normalized(); err != ErrPairing {
			t.Errorf("accepted IP %q", ip)
		}
	}
	for _, mutate := range []func(*Pairing){
		func(p *Pairing) { p.DTLS = false }, func(p *Pairing) { p.UID = "bad" },
		func(p *Pairing) { p.ENR = "short" }, func(p *Pairing) { p.MAC = "a:b:c:d:e:f" },
		func(p *Pairing) { p.Model = "../model" },
	} {
		bad := p
		mutate(&bad)
		if _, err := bad.normalized(); err != ErrPairing {
			t.Error("accepted invalid pairing")
		}
	}
}
