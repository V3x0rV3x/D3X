package mediainfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Exec : le binaire mediainfo (paquet Alpine de l'image Docker, ou installé
// sur le PC). D3X_MEDIAINFO le désigne, sinon il est cherché dans le PATH.
type Exec struct {
	Path string
}

// Find : le binaire, nil s'il n'y en a pas. Un D3X_MEDIAINFO qui désigne un
// fichier absent désactive le binaire au lieu de retomber sur le PATH.
func Find() *Exec {
	if p := strings.TrimSpace(os.Getenv("D3X_MEDIAINFO")); p != "" {
		if _, err := os.Stat(p); err == nil {
			return &Exec{Path: p}
		}
		return nil
	}
	p, err := exec.LookPath("mediainfo")
	if err != nil {
		return nil
	}
	return &Exec{Path: p}
}

// reCompleteName : la ligne qui porte le chemin du fichier. Aucun chemin
// local ne part vers V3X.
var reCompleteName = regexp.MustCompile(`(?m)^(Complete name|Nom complet)\s*:.*\r?\n?`)

// Analyze lance le binaire sur le fichier (source locale seulement) et rend
// sa sortie texte, sans la ligne « Complete name ».
func (e *Exec) Analyze(ctx context.Context, req Request) (string, error) {
	if req.Source != "local" || req.Path == "" {
		return "", errors.New("mediainfo : seulement pour un fichier de ce PC")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Path, req.Path)
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	cmd.WaitDelay = 5 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("mediainfo : délai dépassé")
		}
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New("mediainfo : " + msg)
	}
	text := reCompleteName.ReplaceAllString(out.String(), "")
	return strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), nil
}
