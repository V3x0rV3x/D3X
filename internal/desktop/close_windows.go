//go:build windows

package desktop

import (
	"os/exec"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processCommandLineInformation : classe de NtQueryInformationProcess qui rend
// la ligne de commande d'un processus (Windows 8.1 et plus).
const processCommandLineInformation = 60

// CloseWindow ferme la fenêtre d'application de D3X en arrêtant les processus
// du navigateur lancés avec SON profil (profileDir), et eux seuls : un Edge ou
// un Chrome ouvert par ailleurs n'est jamais touché. Sans ça, la fenêtre
// survivait parfois à D3X, en rectangle blanc ou gris impossible à fermer
// (retours de V3x0r et de CoprinusComatus, 20/10/26). Rend le nombre de
// processus arrêtés, une fois qu'ils sont vraiment partis (3 s au plus) : une
// fenêtre rouverte aussitôt sur le même profil se raccrochait sinon au
// navigateur mourant et restait vide (Luci, 21/10/26).
func CloseWindow(profileDir string) int {
	var stopped []windows.Handle
	defer func() {
		for _, h := range stopped {
			_ = windows.CloseHandle(h)
		}
	}()
	for _, pid := range profilePIDs(profileDir) {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
		if err != nil {
			continue
		}
		if windows.TerminateProcess(h, 0) != nil {
			_ = windows.CloseHandle(h)
			continue
		}
		stopped = append(stopped, h)
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, h := range stopped {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		_, _ = windows.WaitForSingleObject(h, uint32(left/time.Millisecond))
	}
	return len(stopped)
}

// profilePIDs : les processus du navigateur lancés avec le profil de D3X.
func profilePIDs(profileDir string) []uint32 {
	var out []uint32
	for _, p := range profileProcs(profileDir) {
		out = append(out, p.pid)
	}
	return out
}

type proc struct {
	pid uint32
	cmd string // ligne de commande, en minuscules
}

// appBrowserExe : les navigateurs que D3X sait lancer en fenêtre d'application
// (window.go, appBrowser), par nom de processus.
var appBrowserExe = map[string]bool{"msedge.exe": true, "chrome.exe": true, "brave.exe": true, "vivaldi.exe": true}

// profileProcs : ces processus avec leur ligne de commande.
func profileProcs(profileDir string) []proc {
	want := strings.ToLower(profileDir)
	if want == "" {
		return nil
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []proc
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := strings.ToLower(windows.UTF16ToString(e.ExeFile[:]))
		if !appBrowserExe[name] {
			continue
		}
		cmd, ok := commandLine(e.ProcessID)
		if !ok {
			continue
		}
		cmd = strings.ToLower(cmd)
		if usesProfile(cmd, want) {
			out = append(out, proc{pid: e.ProcessID, cmd: cmd})
		}
	}
	return out
}

// usesProfile : la ligne de commande (en minuscules) lance le navigateur avec
// ce profil (en minuscules).
func usesProfile(cmd, profile string) bool {
	return strings.Contains(cmd, "--user-data-dir") && strings.Contains(cmd, profile)
}

// warmUp : quand le navigateur de D3X ne tourne pas encore, il est d'abord
// lancé sans fenêtre, et la fenêtre n'est ouverte qu'une fois qu'il est prêt.
// Chez Luci (21/10/26), la fenêtre créée au démarrage même du navigateur
// restait vide alors que sa page était chargée, et celle ouverte ensuite
// dans le navigateur déjà lancé s'affichait : c'est ce second chemin qui sert
// désormais à chaque fois.
func warmUp(browser, profileDir string) {
	if len(profilePIDs(profileDir)) > 0 {
		return
	}
	cmd := exec.Command(browser, append([]string{"--no-startup-window"}, profileArgs(profileDir)...)...)
	hideConsole(cmd)
	if cmd.Start() != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
	// Prêt = son processus graphique tourne (8 s au plus : une machine lente
	// le lance tard, et c'est sans lui que la fenêtre restait vide), puis
	// une seconde de marge.
	for i := 0; i < 80 && !hasGPUProcess(profileDir); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(time.Second)
}

func hasGPUProcess(profileDir string) bool {
	for _, p := range profileProcs(profileDir) {
		if strings.Contains(p.cmd, "--type=gpu-process") {
			return true
		}
	}
	return false
}

// commandLine lit la ligne de commande d'un processus.
func commandLine(pid uint32) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	buf := make([]byte, 16<<10)
	var n uint32
	if err := windows.NtQueryInformationProcess(h, processCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &n); err != nil {
		if n <= uint32(len(buf)) || n > 1<<20 {
			return "", false
		}
		buf = make([]byte, n)
		if err := windows.NtQueryInformationProcess(h, processCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &n); err != nil {
			return "", false
		}
	}
	us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
	if us.Buffer == nil || us.Length == 0 {
		return "", false
	}
	return windows.UTF16ToString(unsafe.Slice(us.Buffer, int(us.Length)/2)), true
}
