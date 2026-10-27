package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/V3x0rV3x/D3X/internal/pathmap"
)

// Une configuration de D3X 1.0 (un seul client, champs « qbit ») devient une
// liste à un client, sans rien perdre, et toujours avec le même identifiant.
func TestMigrateOneClient(t *testing.T) {
	old := `{
  "v3xKey": "cle",
  "clientKind": "deluge",
  "qbitUrl": "https://box.exemple.net/deluge",
  "qbitUser": "",
  "qbitPass": "secret",
  "qbitInsecure": true,
  "category": "v3x",
  "tag": "D3X",
  "pathMap": [{"from": "Z:\\Films", "to": "/data/films"}],
  "links": {"on": true, "dir": "/data/liens"},
  "watch": {"on": true, "notify": true}
}`
	var ids []string
	for i := 0; i < 2; i++ {
		c := Defaults()
		if err := Parse([]byte(old), c); err != nil {
			t.Fatal(err)
		}
		if len(c.Clients) != 1 {
			t.Fatalf("%d clients, 1 attendu", len(c.Clients))
		}
		cl := c.Clients[0]
		if cl.Kind != "deluge" || cl.URL != "https://box.exemple.net/deluge" || cl.Pass != "secret" || !cl.Insecure || cl.LinksDir != "/data/liens" {
			t.Fatalf("client migré : %+v", cl)
		}
		if len(cl.PathMap) != 1 || cl.PathMap[0] != (pathmap.Rule{From: `Z:\Films`, To: "/data/films"}) {
			t.Fatalf("dossiers du client : %+v", cl.PathMap)
		}
		if c.Category != "v3x" || !c.Links.On || !c.Watch.On || c.V3XKey != "cle" {
			t.Fatalf("autres réglages perdus : %+v", c)
		}
		ids = append(ids, cl.ID)
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("identifiants : %v (deux lectures doivent donner le même)", ids)
	}
}

// Sans client : la configuration reste valide, aucun client inventé.
func TestMigrateNoClient(t *testing.T) {
	c := Defaults()
	if err := Parse([]byte(`{"v3xKey":"cle","qbitUrl":"","qbitUser":"","qbitPass":""}`), c); err != nil {
		t.Fatal(err)
	}
	if len(c.Clients) != 0 {
		t.Fatalf("clients : %+v", c.Clients)
	}
	b, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	back := Defaults()
	if err := Parse(b, back); err != nil || len(back.Clients) != 0 {
		t.Fatalf("relu : %+v %v", back.Clients, err)
	}
}

// À l'enregistrement, les anciens champs reflètent le premier client : D3X 1.0
// relit son client ; un client retiré ne revient pas à la lecture suivante.
func TestMirrorFirstClient(t *testing.T) {
	c := Defaults()
	c.Clients = []ClientConfig{
		{ID: "a", Name: "Seedbox", Kind: "deluge", URL: "https://box/deluge", Pass: "p1", PathMap: []pathmap.Rule{{From: "S:", To: "/home/moi"}}, LinksDir: "/home/moi/liens"},
		{ID: "b", Name: "PC", URL: "http://127.0.0.1:8080", User: "admin", Pass: "p2"},
	}
	b, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if c.QbitURL != "" {
		t.Fatal("Marshal ne doit pas modifier la configuration en mémoire")
	}
	// Ce que lirait D3X 1.0.
	var v1 struct {
		ClientKind string         `json:"clientKind"`
		QbitURL    string         `json:"qbitUrl"`
		QbitPass   string         `json:"qbitPass"`
		PathMap    []pathmap.Rule `json:"pathMap"`
		Links      Links          `json:"links"`
	}
	if err := json.Unmarshal(b, &v1); err != nil {
		t.Fatal(err)
	}
	if v1.ClientKind != "deluge" || v1.QbitURL != "https://box/deluge" || v1.QbitPass != "p1" || len(v1.PathMap) != 1 || v1.Links.Dir != "/home/moi/liens" {
		t.Fatalf("anciens champs : %+v", v1)
	}
	// Relu par cette version : la liste fait foi, les deux clients reviennent.
	back := Defaults()
	if err := Parse(b, back); err != nil {
		t.Fatal(err)
	}
	if len(back.Clients) != 2 || back.Clients[1].Name != "PC" || back.Clients[1].Pass != "p2" || back.Client("a") == nil {
		t.Fatalf("relu : %+v", back.Clients)
	}
	// Tous les clients retirés : plus rien à migrer.
	back.Clients = nil
	b, _ = Marshal(back)
	if strings.Contains(string(b), "box/deluge") {
		t.Fatalf("un client retiré reste dans le fichier : %s", b)
	}
	again := Defaults()
	if err := Parse(b, again); err != nil || len(again.Clients) != 0 {
		t.Fatalf("client retiré revenu : %+v %v", again.Clients, err)
	}
}

// Identifiants manquants ou en double : chacun le sien.
func TestClientIDs(t *testing.T) {
	c := Defaults()
	if err := Parse([]byte(`{"clients":[{"url":"http://a"},{"id":"x","url":"http://b"},{"id":"x","url":"http://c"}]}`), c); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, cl := range c.Clients {
		if cl.ID == "" || seen[cl.ID] {
			t.Fatalf("identifiants : %+v", c.Clients)
		}
		seen[cl.ID] = true
	}
	if c.Clients[1].ID != "x" {
		t.Fatalf("un identifiant valable ne change pas : %+v", c.Clients)
	}
	if a, b := NewClientID(), NewClientID(); a == b || len(a) != 12 {
		t.Fatalf("NewClientID : %s %s", a, b)
	}
}

// Seedboxes enregistrées : la dernière en tête, sans doublon, plafonnées ;
// oublier n'en retire qu'une.
func TestRememberSSH(t *testing.T) {
	var c Config
	a := SSH{Host: "a.example", User: "u"}
	b := SSH{Host: "b.example", Port: 2222, User: "u"}
	c.RememberSSH(a)
	c.RememberSSH(b)
	c.RememberSSH(SSH{Host: "A.example", User: "u", HostKey: "k"})
	if len(c.SSHSaved) != 2 || c.SSHSaved[0].HostKey != "k" || !c.SSHSaved[1].Same(b) {
		t.Fatalf("liste inattendue : %+v", c.SSHSaved)
	}
	for i := 0; i < 20; i++ {
		c.RememberSSH(SSH{Host: "h" + string(rune('a'+i)), User: "u"})
	}
	if len(c.SSHSaved) != MaxSSHSaved {
		t.Fatalf("%d seedboxes gardées, attendu %d", len(c.SSHSaved), MaxSSHSaved)
	}
	c.ForgetSSH(c.SSHSaved[0])
	if len(c.SSHSaved) != MaxSSHSaved-1 {
		t.Fatal("ForgetSSH n'a pas retiré la seedbox")
	}
}

// Intervalles de la surveillance, jusqu'à une fois par jour (Ekhstaz,
// 27/10/26) ; au-delà ou en deçà, borné.
func TestWatchEvery(t *testing.T) {
	for in, want := range map[int]int{0: 30, 5: 15, 15: 15, 180: 180, 360: 360, 720: 720, 1440: 1440, 5000: 1440} {
		if got := (Watch{Minutes: in}).Every(); got != want {
			t.Errorf("%d min : %d, attendu %d", in, got, want)
		}
	}
}
