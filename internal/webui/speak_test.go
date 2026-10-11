package webui

import "testing"

func TestGo2rtcAudioArg(t *testing.T) {
	for in, want := range map[string]string{"PCML/16000/1": "pcml/16000", "PCMU/8000": "pcmu/8000", "OPUS": "opus", "": "pcml/16000"} {
		if got := go2rtcAudioArg(in); got != want {
			t.Errorf("go2rtcAudioArg(%q) = %q want %q", in, got, want)
		}
	}
}

func TestFirstStreamAuth(t *testing.T) {
	for in, want := range map[string]string{"u:p": "u:p", "u:p@cam1,cam2|v:q": "u:p", "": ""} {
		if got := firstStreamAuth(in); got != want {
			t.Errorf("firstStreamAuth(%q) = %q want %q", in, got, want)
		}
	}
}
