package lsp

import "encoding/json"

// JSON-RPC and LSP error codes (spec §3).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeNotInitialized = -32002
)

// rpcError is a response error.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// response is a JSON-RPC response; Result is "null" for a null result.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// notification is a JSON-RPC notification the server sends.
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// Location is a range of a document.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// DiagnosticRelatedInformation is a related location of a diagnostic.
type DiagnosticRelatedInformation struct {
	Location Location `json:"location"`
	Message  string   `json:"message"`
}

// Diagnostic is an LSP diagnostic (spec R3).
type Diagnostic struct {
	Range              Range                          `json:"range"`
	Severity           int                            `json:"severity"`
	Code               string                         `json:"code"`
	Source             string                         `json:"source"`
	Message            string                         `json:"message"`
	RelatedInformation []DiagnosticRelatedInformation `json:"relatedInformation,omitempty"`
}

// PublishDiagnosticsParams is the publishDiagnostics notification's params.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// initializeParams holds the fields of initialize the server reads.
type initializeParams struct {
	Capabilities struct {
		General struct {
			PositionEncodings []string `json:"positionEncodings"`
		} `json:"general"`
		TextDocument struct {
			CodeAction struct {
				CodeActionLiteralSupport json.RawMessage `json:"codeActionLiteralSupport"`
			} `json:"codeAction"`
		} `json:"textDocument"`
	} `json:"capabilities"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type didOpenParams struct {
	TextDocument struct {
		URI     string `json:"uri"`
		Version int    `json:"version"`
		Text    string `json:"text"`
	} `json:"textDocument"`
}

type contentChange struct {
	Range *Range `json:"range"`
	Text  string `json:"text"`
}

type didChangeParams struct {
	TextDocument struct {
		URI     string `json:"uri"`
		Version int    `json:"version"`
	} `json:"textDocument"`
	ContentChanges []contentChange `json:"contentChanges"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type textDocumentPositionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type documentFormattingParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type codeActionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      struct {
		Only []string `json:"only"`
	} `json:"context"`
}

// MarkupContent is hover text.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Hover is the hover result.
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    Range         `json:"range"`
}

// WorkspaceEdit maps document URIs to their edits.
type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`
}

// CodeAction is a quick fix (spec R7).
type CodeAction struct {
	Title       string        `json:"title"`
	Kind        string        `json:"kind"`
	Diagnostics []Diagnostic  `json:"diagnostics"`
	Edit        WorkspaceEdit `json:"edit"`
}

// logMessageParams is window/logMessage's params.
type logMessageParams struct {
	Type    int    `json:"type"`
	Message string `json:"message"`
}
