package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/V3x0rV3x/D3X/internal/torrent"
)

// rTorrent, par XML-RPC (souvent « https://seedbox/RPC2 », derrière une
// authentification HTTP). Pas d'export du .torrent.
type rtorrent struct {
	url  string
	user string
	pass string
	hc   *http.Client
}

func newRTorrent(o Options) (Client, error) {
	u, err := normURL(o.URL)
	if err != nil {
		return nil, err
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/RPC2"
	}
	user, pass := o.User, o.Pass
	if u.User != nil && user == "" {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	u.User = nil
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if o.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // choix du membre
	}
	return &rtorrent{url: u.String(), user: user, pass: pass, hc: &http.Client{Timeout: 2 * time.Minute, Transport: tr}}, nil
}

func (r *rtorrent) Kind() string { return "rTorrent" }

// ---------- XML-RPC minimal ----------

type b64 []byte

func xmlValue(b *bytes.Buffer, v interface{}) {
	b.WriteString("<value>")
	switch x := v.(type) {
	case string:
		b.WriteString("<string>")
		xml.EscapeText(b, []byte(x))
		b.WriteString("</string>")
	case int:
		b.WriteString("<i8>" + strconv.Itoa(x) + "</i8>")
	case b64:
		b.WriteString("<base64>" + base64.StdEncoding.EncodeToString(x) + "</base64>")
	case []interface{}:
		b.WriteString("<array><data>")
		for _, e := range x {
			xmlValue(b, e)
		}
		b.WriteString("</data></array>")
	case map[string]interface{}:
		b.WriteString("<struct>")
		for k, e := range x {
			b.WriteString("<member><name>")
			xml.EscapeText(b, []byte(k))
			b.WriteString("</name>")
			xmlValue(b, e)
			b.WriteString("</member>")
		}
		b.WriteString("</struct>")
	}
	b.WriteString("</value>")
}

// node : un élément XML lu tel quel.
type node struct {
	XMLName xml.Name
	Content string `xml:",chardata"`
	Nodes   []node `xml:",any"`
}

func (n node) child(name string) *node {
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == name {
			return &n.Nodes[i]
		}
	}
	return nil
}

// decode transforme un <value> en string, int64, bool, []interface{} ou map.
func decodeValue(v node) interface{} {
	if len(v.Nodes) == 0 {
		return v.Content // <value>texte</value> vaut une chaîne
	}
	t := v.Nodes[0]
	switch t.XMLName.Local {
	case "string":
		return t.Content
	case "i4", "i8", "int":
		n, _ := strconv.ParseInt(strings.TrimSpace(t.Content), 10, 64)
		return n
	case "boolean":
		return strings.TrimSpace(t.Content) == "1"
	case "double":
		f, _ := strconv.ParseFloat(strings.TrimSpace(t.Content), 64)
		return f
	case "base64":
		b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(t.Content))
		return string(b)
	case "array":
		var out []interface{}
		if d := t.child("data"); d != nil {
			for _, e := range d.Nodes {
				out = append(out, decodeValue(e))
			}
		}
		return out
	case "struct":
		out := map[string]interface{}{}
		for _, m := range t.Nodes {
			name, val := m.child("name"), m.child("value")
			if name != nil && val != nil {
				out[name.Content] = decodeValue(*val)
			}
		}
		return out
	}
	return t.Content
}

func (r *rtorrent) call(ctx context.Context, method string, params ...interface{}) (interface{}, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodCall><methodName>`)
	xml.EscapeText(&b, []byte(method))
	b.WriteString("</methodName><params>")
	for _, p := range params {
		b.WriteString("<param>")
		xmlValue(&b, p)
		b.WriteString("</param>")
	}
	b.WriteString("</params></methodCall>")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.url, &b)
	req.Header.Set("Content-Type", "text/xml")
	if r.user != "" || r.pass != "" {
		req.SetBasicAuth(r.user, r.pass)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rTorrent injoignable : %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrAuth
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rTorrent : HTTP %d", resp.StatusCode)
	}
	var root node
	if err := xml.Unmarshal(data, &root); err != nil || root.XMLName.Local != "methodResponse" {
		return nil, errors.New("ce n'est pas rTorrent (XML-RPC) qui répond à cette adresse")
	}
	if f := root.child("fault"); f != nil {
		if v := f.child("value"); v != nil {
			if m, ok := decodeValue(*v).(map[string]interface{}); ok {
				return nil, fmt.Errorf("rTorrent : %v", m["faultString"])
			}
		}
		return nil, errors.New("rTorrent : erreur")
	}
	if ps := root.child("params"); ps != nil {
		if p := ps.child("param"); p != nil {
			if v := p.child("value"); v != nil {
				return decodeValue(*v), nil
			}
		}
	}
	return nil, nil
}

func asString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}

func asInt(v interface{}) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

// ---------- interface ----------

func (r *rtorrent) Version(ctx context.Context) (string, error) {
	v, err := r.call(ctx, "system.client_version")
	return asString(v), err
}

var rtFields = []string{"d.hash=", "d.name=", "d.directory=", "d.size_bytes=", "d.completed_bytes=", "d.state=", "d.hashing=", "d.complete=", "d.is_multi_file=", "d.custom1="}

func (r *rtorrent) List(ctx context.Context, hashes []string) ([]Torrent, error) {
	params := []interface{}{"", "main"}
	for _, f := range rtFields {
		params = append(params, f)
	}
	v, err := r.call(ctx, "d.multicall2", params...)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, h := range hashes {
		want[strings.ToLower(h)] = true
	}
	rows, _ := v.([]interface{})
	var out []Torrent
	for _, row := range rows {
		f, _ := row.([]interface{})
		if len(f) < len(rtFields) {
			continue
		}
		h := strings.ToLower(asString(f[0]))
		if len(want) > 0 && !want[h] {
			continue
		}
		name, dir := asString(f[1]), asString(f[2])
		size, done := asInt(f[3]), asInt(f[4])
		content := dir
		if asInt(f[8]) == 0 {
			content = joinPath(dir, name) // mono-fichier : d.directory est le dossier parent
		}
		p := 0.0
		if size > 0 {
			p = float64(done) / float64(size)
		}
		out = append(out, Torrent{
			Hash: h, Hashes: []string{h}, Name: name, ContentPath: content, Size: size, TotalSize: size, Progress: p,
			Checking:    asInt(f[6]) != 0,
			Downloading: asInt(f[5]) == 1 && asInt(f[7]) == 0,
			Stopped:     asInt(f[5]) == 0,
			Tags:        tagList([]string{asString(f[9])}), // l'étiquette de ruTorrent
		})
	}
	// Trackers, par lots d'appels groupés.
	for i := 0; i < len(out); i += 300 {
		end := i + 300
		if end > len(out) {
			end = len(out)
		}
		var calls []interface{}
		for _, t := range out[i:end] {
			calls = append(calls, map[string]interface{}{"methodName": "t.multicall", "params": []interface{}{strings.ToUpper(t.Hash), "", "t.url="}})
		}
		res, err := r.call(ctx, "system.multicall", calls)
		if err != nil {
			break // les trackers ne servent qu'à reconnaître V3X : on s'en passe
		}
		list, _ := res.([]interface{})
		for j, item := range list {
			// Chaque résultat est [valeur] ; la valeur est une liste de [url].
			wrap, _ := item.([]interface{})
			if len(wrap) == 0 || i+j >= len(out) {
				continue
			}
			trs, _ := wrap[0].([]interface{})
			var urls []string
			for _, tr := range trs {
				if row, ok := tr.([]interface{}); ok && len(row) > 0 {
					urls = append(urls, asString(row[0]))
				}
			}
			out[i+j].Trackers = TrackerHosts(urls)
		}
	}
	return out, nil
}

func (r *rtorrent) Export(context.Context, string) ([]byte, error) { return nil, ErrNoExport }

func (r *rtorrent) Files(ctx context.Context, hash string) ([]torrent.File, int64, error) {
	list, err := r.List(ctx, []string{hash})
	if err != nil {
		return nil, 0, err
	}
	if len(list) == 0 {
		return nil, 0, errors.New("torrent introuvable")
	}
	t := list[0]
	v, err := r.call(ctx, "f.multicall", strings.ToUpper(hash), "", "f.path=", "f.size_bytes=")
	if err != nil {
		return nil, 0, err
	}
	rows, _ := v.([]interface{})
	var out []torrent.File
	mv, err := r.call(ctx, "d.is_multi_file", strings.ToUpper(hash))
	if err != nil {
		return nil, 0, err
	}
	multi := asInt(mv) != 0
	for i, row := range rows {
		f, _ := row.([]interface{})
		if len(f) < 2 {
			continue
		}
		path := asString(f[0])
		if multi {
			path = t.Name + "/" + path
		}
		out = append(out, torrent.File{Path: path, Size: asInt(f[1]), Index: i})
	}
	return out, t.TotalSize, nil
}

func (r *rtorrent) Add(ctx context.Context, name string, raw []byte, o AddOptions) error {
	t, err := torrent.Parse(raw)
	if err != nil {
		return err
	}
	if list, err := r.List(ctx, []string{t.InfoHash}); err == nil && len(list) > 0 {
		return ErrAlreadyThere
	}
	params := []interface{}{"", b64(raw)}
	if o.SavePath != "" {
		params = append(params, `d.directory.set=`+rtQuote(o.SavePath))
	}
	if o.Tag != "" {
		params = append(params, "d.custom1.set="+rtQuote(o.Tag)) // étiquette de ruTorrent
	}
	// load.raw : ajouté sans démarrer.
	_, err = r.call(ctx, "load.raw", params...)
	return err
}

func (r *rtorrent) one(ctx context.Context, method, h string) error {
	_, err := r.call(ctx, method, strings.ToUpper(h))
	return err
}

func (r *rtorrent) Recheck(ctx context.Context, h string) error { return r.one(ctx, "d.check_hash", h) }
func (r *rtorrent) Start(ctx context.Context, h string) error   { return r.one(ctx, "d.start", h) }
func (r *rtorrent) Stop(ctx context.Context, h string) error    { return r.one(ctx, "d.stop", h) }

// RemoveKeepFiles : avec rTorrent, D3X arrête et ferme le torrent mais ne le
// retire pas. Un réglage de rTorrent (événement « erased ») peut effacer les
// données au retrait, et ces données sont celles de ta copie : à retirer
// toi-même dans ruTorrent, en gardant les fichiers.
func (r *rtorrent) RemoveKeepFiles(ctx context.Context, h string) error {
	_ = r.one(ctx, "d.stop", h)
	_ = r.one(ctx, "d.close", h)
	return errors.New("torrent arrêté : retire-le toi-même dans ruTorrent en gardant les fichiers (rTorrent peut être réglé pour effacer les données au retrait, D3X ne prend pas ce risque)")
}

// rtQuote : une valeur entre guillemets pour une commande de rTorrent
// (barres obliques inverses et guillemets échappés).
func rtQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
