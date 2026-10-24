package ftransport

import (
	"crypto/tls"
	"net/http"
	"reflect"
	"testing"
)

func TestExplicitHTTP2SettingsPreserveEmptyAndZero(t *testing.T) {
	t.Parallel()

	empty := Config{HTTP2: HTTP2Config{InitialSettings: ExplicitHTTP2Settings()}}
	if err := empty.Validate(); err != nil {
		t.Fatalf("explicit empty settings: %v", err)
	}
	clone := empty.Clone()
	if !clone.HTTP2.InitialSettings.Present {
		t.Fatal("clone lost explicit settings presence")
	}
	if clone.HTTP2.InitialSettings.Values == nil || len(clone.HTTP2.InitialSettings.Values) != 0 {
		t.Fatalf("clone did not preserve explicit empty slice: %#v", clone.HTTP2.InitialSettings.Values)
	}

	zero := Config{HTTP2: HTTP2Config{InitialSettings: ExplicitHTTP2Settings(
		HTTP2Setting{ID: SettingInitialWindowSize, Value: 0},
	)}}
	if err := zero.Validate(); err != nil {
		t.Fatalf("explicit zero setting: %v", err)
	}
	if got := zero.Clone().HTTP2.InitialSettings.Values[0].Value; got != 0 {
		t.Fatalf("explicit zero became %d", got)
	}
}

func TestConfigCloneIsDeep(t *testing.T) {
	t.Parallel()

	config := Config{
		Protocols: []Protocol{ProtocolHTTP2},
		HTTP1:     HTTP1Config{HeaderOrder: []string{"Accept"}},
		TLSConfig: &tls.Config{
			NextProtos:                     []string{"h2"},
			CipherSuites:                   []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
			EncryptedClientHelloConfigList: []byte{1, 2, 3},
			Certificates: []tls.Certificate{{
				Certificate: [][]byte{{1, 2, 3}},
				OCSPStaple:  []byte{4, 5, 6},
			}},
		},
		HTTP2: HTTP2Config{
			InitialSettings:   ExplicitHTTP2Settings(HTTP2Setting{ID: SettingEnablePush, Value: 0}),
			PriorityFrames:    []HTTP2PriorityFrame{{StreamID: 3, HTTP2PriorityParam: HTTP2PriorityParam{Weight: 16}}},
			HeaderPriority:    &HTTP2PriorityParam{Weight: 32},
			PseudoHeaderOrder: []PseudoHeader{PseudoHeaderMethod, PseudoHeaderPath},
			HeaderOrder:       []string{"accept"},
		},
	}
	config.TLSConfig.NameToCertificate = map[string]*tls.Certificate{"example.com": &config.TLSConfig.Certificates[0]}
	clone := config.Clone()

	config.Protocols[0] = ProtocolHTTP1
	config.HTTP1.HeaderOrder[0] = "Changed"
	config.HTTP2.InitialSettings.Values[0].Value = 1
	config.HTTP2.PriorityFrames[0].Weight = 1
	config.HTTP2.HeaderPriority.Weight = 1
	config.HTTP2.PseudoHeaderOrder[0] = PseudoHeaderAuthority
	config.HTTP2.HeaderOrder[0] = "changed"
	config.TLSConfig.NextProtos[0] = "changed"
	config.TLSConfig.CipherSuites[0] = 0
	config.TLSConfig.EncryptedClientHelloConfigList[0] = 0
	config.TLSConfig.Certificates[0].Certificate[0][0] = 0
	config.TLSConfig.Certificates[0].OCSPStaple[0] = 0

	if clone.Protocols[0] != ProtocolHTTP2 || clone.HTTP1.HeaderOrder[0] != "Accept" ||
		clone.HTTP2.InitialSettings.Values[0].Value != 0 || clone.HTTP2.PriorityFrames[0].Weight != 16 ||
		clone.HTTP2.HeaderPriority.Weight != 32 || clone.HTTP2.PseudoHeaderOrder[0] != PseudoHeaderMethod ||
		clone.HTTP2.HeaderOrder[0] != "accept" || clone.TLSConfig.NextProtos[0] != "h2" ||
		clone.TLSConfig.CipherSuites[0] == 0 || clone.TLSConfig.EncryptedClientHelloConfigList[0] != 1 ||
		clone.TLSConfig.Certificates[0].Certificate[0][0] != 1 || clone.TLSConfig.Certificates[0].OCSPStaple[0] != 4 ||
		clone.TLSConfig.NameToCertificate["example.com"] != &clone.TLSConfig.Certificates[0] {
		t.Fatalf("clone shares mutable configuration: %#v", clone)
	}
}

func TestConfigValidationRejectsAmbiguity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
	}{
		{
			name:   "settings without presence",
			config: Config{HTTP2: HTTP2Config{InitialSettings: HTTP2Settings{Values: []HTTP2Setting{{ID: SettingEnablePush}}}}},
		},
		{
			name: "duplicate settings",
			config: Config{HTTP2: HTTP2Config{InitialSettings: ExplicitHTTP2Settings(
				HTTP2Setting{ID: SettingEnablePush, Value: 0},
				HTTP2Setting{ID: SettingEnablePush, Value: 1},
			)}},
		},
		{
			name:   "duplicate header case folded",
			config: Config{HTTP1: HTTP1Config{HeaderOrder: []string{"Accept", "accept"}}},
		},
		{
			name:   "self dependent priority",
			config: Config{HTTP2: HTTP2Config{PriorityFrames: []HTTP2PriorityFrame{{StreamID: 3, HTTP2PriorityParam: HTTP2PriorityParam{Dependency: 3, Weight: 1}}}}},
		},
		{
			name:   "static header priority future self dependency",
			config: Config{HTTP2: HTTP2Config{HeaderPriority: &HTTP2PriorityParam{Dependency: 3, Weight: 1}}},
		},
		{
			name:   "server push unsupported",
			config: Config{HTTP2: HTTP2Config{AllowServerPush: true}},
		},
		{
			name: "server push setting unsupported",
			config: Config{HTTP2: HTTP2Config{InitialSettings: ExplicitHTTP2Settings(
				HTTP2Setting{ID: SettingEnablePush, Value: 1},
			)}},
		},
		{
			name:   "connection window overflow",
			config: Config{HTTP2: HTTP2Config{ConnectionWindowIncrement: 0x7fffffff - 65535 + 1}},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.config.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRequestOptionsCloneAndIsolation(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	options := RequestOptions{
		HeaderOrder:       []string{"User-Agent", "Accept"},
		PseudoHeaderOrder: []PseudoHeader{PseudoHeaderMethod, PseudoHeaderAuthority, PseudoHeaderScheme, PseudoHeaderPath},
		SensitiveHeaders:  []string{"Authorization"},
	}
	clone, err := WithRequestOptions(req, options)
	if err != nil {
		t.Fatal(err)
	}
	options.HeaderOrder[0] = "Changed"

	got, ok := RequestOptionsFromRequest(clone)
	if !ok {
		t.Fatal("request options missing")
	}
	if !reflect.DeepEqual(got.HeaderOrder, []string{"User-Agent", "Accept"}) {
		t.Fatalf("stored options changed: %#v", got.HeaderOrder)
	}
	got.HeaderOrder[0] = "Mutated"
	gotAgain, _ := RequestOptionsFromRequest(clone)
	if gotAgain.HeaderOrder[0] != "User-Agent" {
		t.Fatal("returned request options were not defensively copied")
	}
	if _, ok := RequestOptionsFromRequest(req); ok {
		t.Fatal("original request was modified")
	}
}
