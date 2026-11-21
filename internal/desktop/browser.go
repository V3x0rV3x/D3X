package desktop

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Réglage « Ouvrir les liens avec » (YataReborn, 21/11/26) : les liens de
// D3X (fiches V3X, « Ouvrir dans le navigateur ») partent dans le navigateur
// par défaut de la machine, donc dans son profil par défaut. Un membre qui
// garde V3X dans un profil à part (Firefox -profile, un autre navigateur)
// donne ici la commande : programme puis arguments, l'adresse s'ajoute à la
// fin. Aucun interpréteur de commandes n'est lancé : le programme reçoit ses
// arguments tels quels.

// SplitCommand découpe une ligne de commande en mots. Des guillemets doubles
// gardent ensemble un chemin ou un argument qui contient des espaces ; ils
// ne font pas partie du mot.
func SplitCommand(s string) []string {
	var out []string
	var cur strings.Builder
	quoted, inWord := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			inWord = true
		case !quoted && (r == ' ' || r == '\t'):
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// CheckBrowser vérifie la commande du réglage : vide (navigateur par défaut),
// ou un programme désigné par son chemin complet, qui existe. Sous Windows,
// un .exe seulement.
func CheckBrowser(cmd string) error {
	parts := SplitCommand(strings.TrimSpace(cmd))
	if len(parts) == 0 {
		return nil
	}
	exe := parts[0]
	if !filepath.IsAbs(exe) {
		return errors.New("navigateur : donne le chemin complet du programme, entre guillemets s'il contient des espaces")
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(exe), ".exe") {
		return errors.New("navigateur : le programme doit être un .exe")
	}
	st, err := os.Stat(exe)
	if err != nil || !st.Mode().IsRegular() {
		return errors.New("navigateur : programme introuvable (" + exe + ")")
	}
	return nil
}

// OpenWith ouvre l'adresse avec la commande du réglage, ou dans le navigateur
// par défaut si elle est vide.
func OpenWith(cmd, url string) error {
	parts := SplitCommand(strings.TrimSpace(cmd))
	if len(parts) == 0 {
		return OpenDefault(url)
	}
	if err := CheckBrowser(cmd); err != nil {
		return err
	}
	c := exec.Command(parts[0], append(parts[1:], url)...)
	noConsole(c)
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}
