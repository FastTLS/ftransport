// Package ftransport provides the HTTP transport contract used by fastls.
//
// The package has completed its F2 HTTP/1.1 and HTTP/2 implementation phase.
// Transport supports deterministic H1 field order and configurable H2 startup
// frames, header order, priority, and HPACK sensitivity.
package ftransport
