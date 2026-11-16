package dlna

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type soapAction struct {
	Name       string
	ServiceURN string
	Args       map[string]string
}

// parseSOAP 同时兼容带 URN 前缀与不带前缀的 SOAPACTION 头。
func parseSOAP(r *http.Request) (soapAction, error) {
	res := soapAction{Args: map[string]string{}}
	h := strings.TrimSpace(r.Header.Get("SOAPACTION"))
	if h == "" {
		h = strings.TrimSpace(r.Header.Get("SOAPAction"))
	}
	h = strings.Trim(h, `"`)
	if i := strings.LastIndexByte(h, '#'); i >= 0 {
		res.ServiceURN = h[:i]
		res.Name = h[i+1:]
	} else if h != "" {
		res.Name = h
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return res, err
	}
	name, args, err := parseSOAPBody(body)
	if err != nil {
		return res, err
	}
	if name != "" {
		res.Name = name // body 中的 action 更可靠
	}
	if res.Name == "" {
		return res, errors.New("无法确定 SOAP action")
	}
	res.Args = args
	return res, nil
}

// parseSOAPBody 提取 Envelope/Body 下的 action 名及其直接子元素参数。
func parseSOAPBody(body []byte) (string, map[string]string, error) {
	args := make(map[string]string)
	dec := xml.NewDecoder(bytes.NewReader(body))
	action := ""
	inBody := false
	depth := 0
	argName := ""
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("解析 SOAP body 失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "Body" {
				inBody = true
				depth = 0
				continue
			}
			if !inBody {
				continue
			}
			if action == "" {
				action = t.Name.Local
				depth = 1
				continue
			}
			depth++
			if depth == 2 {
				argName = t.Name.Local
				buf.Reset()
			}
		case xml.CharData:
			if inBody && argName != "" && depth == 2 {
				buf.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "Body" {
				inBody = false
				continue
			}
			if !inBody {
				continue
			}
			if depth == 2 && t.Name.Local == argName {
				args[argName] = buf.String()
				argName = ""
			}
			depth--
		}
	}
	if action == "" {
		return "", nil, errors.New("SOAP body 中没有 action")
	}
	return action, args, nil
}

func writeActionResponse(w http.ResponseWriter, urn, action string, args [][2]string) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<s:Envelope xmlns:s="` + SOAPEnvelopeNS + `" s:encodingStyle="` + SOAPEncoding + `"><s:Body>`)
	b.WriteString(`<u:` + action + `Response xmlns:u="` + urn + `">`)
	for _, kv := range args {
		b.WriteString("<" + kv[0] + ">" + xmlEscape(kv[1]) + "</" + kv[0] + ">")
	}
	b.WriteString(`</u:` + action + `Response></s:Body></s:Envelope>`)
	w.Header().Set("EXT", "")
	writeXMLBody(w, http.StatusOK, b.String())
}

func writeSOAPFault(w http.ResponseWriter, code int, desc string) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<s:Envelope xmlns:s="` + SOAPEnvelopeNS + `" s:encodingStyle="` + SOAPEncoding + `">` +
		`<s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>` +
		`<UPnPError xmlns="` + UPnPControlNS + `"><errorCode>` + fmt.Sprint(code) + `</errorCode>` +
		`<errorDescription>` + xmlEscape(desc) + `</errorDescription></UPnPError>` +
		`</detail></s:Fault></s:Body></s:Envelope>`
	w.Header().Set("EXT", "")
	writeXMLBody(w, http.StatusInternalServerError, body)
}
