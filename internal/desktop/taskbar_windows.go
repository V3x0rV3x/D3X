//go:build windows

package desktop

import (
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/V3x0rV3x/D3X/internal/update"
	"golang.org/x/sys/windows"
)

// appID : l'identifiant de D3X dans la barre des tâches de Windows
// (AppUserModelID). La fenêtre d'application le porte à la place de celui que
// lui donne Edge ou Chrome : épinglée, elle garde l'icône de D3X et relance
// D3X.exe, et non plus le navigateur sur l'adresse locale d'un ancien
// lancement, qui change à chaque fois (retour d'un membre, 22/10/26).
const appID = "V3X.D3X"

const (
	// identityWait : la fenêtre met un peu de temps à apparaître (navigateur
	// lancé à froid, machine lente) ; au-delà, elle garde l'identité du
	// navigateur, comme avant.
	identityWait = 15 * time.Second
	// identityPoll : une fenêtre trouvée vite reste peu de temps rangée avec
	// le navigateur dans la barre des tâches.
	identityPoll = 100 * time.Millisecond
	// identitySettle : une fois l'identité posée, la fenêtre est encore
	// surveillée un moment, au cas où le navigateur remettrait la sienne.
	identitySettle = 3 * time.Second
)

var (
	pPropStoreForWindow = windows.NewLazySystemDLL("shell32.dll").NewProc("SHGetPropertyStoreForWindow")
	pPropVariantClear   = windows.NewLazySystemDLL("ole32.dll").NewProc("PropVariantClear")

	// IID_IPropertyStore, {886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99}.
	iidPropertyStore = windows.GUID{Data1: 0x886d8eeb, Data2: 0x8cf2, Data3: 0x4446, Data4: [8]byte{0x8d, 0x02, 0xcd, 0xba, 0x1d, 0xbd, 0xcf, 0x99}}

	// Les clés PKEY_AppUserModel_* (propkey.h), toutes du même jeu
	// {9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3}.
	keyRelaunchCommand = appModelKey(2)
	keyRelaunchIcon    = appModelKey(3)
	keyRelaunchName    = appModelKey(4)
	keyAppID           = appModelKey(5)
)

// propertyKey : PROPERTYKEY (20 octets : le GUID du jeu, puis le numéro).
type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

func appModelKey(pid uint32) propertyKey {
	return propertyKey{
		fmtid: windows.GUID{Data1: 0x9f4c2855, Data2: 0x9f79, Data3: 0x4b39, Data4: [8]byte{0xa8, 0xd0, 0xe1, 0xd4, 0x2d, 0xe1, 0xd5, 0xf3}},
		pid:   pid,
	}
}

// vtLPWSTR : PROPVARIANT qui porte une chaîne UTF-16 terminée par un zéro.
const vtLPWSTR = 31

// propVariant : PROPVARIANT, 24 octets en 64 bits (16 en 32 bits) : le type,
// trois mots réservés, puis l'union de 16 octets (8 en 32 bits), dont D3X ne
// lit que le premier mot, le pointeur de chaîne. La valeur est gardée en
// uintptr : une valeur d'un autre type rendue par GetValue n'est jamais prise
// pour un pointeur par le ramasse-miettes de Go.
type propVariant struct {
	vt  uint16
	_   [3]uint16
	val uintptr
	_   uintptr
}

// propertyStore : un IPropertyStore, vu par sa table de méthodes, dans l'ordre
// de propsys.h : QueryInterface, AddRef, Release (IUnknown), puis GetCount,
// GetAt, GetValue, SetValue, Commit.
type propertyStore struct{ vtbl *[8]uintptr }

const (
	psRelease  = 2
	psGetValue = 5
	psSetValue = 6
	psCommit   = 7
)

// windowStore : les propriétés de la fenêtre pour la barre des tâches, nil si
// Windows ne les donne pas.
func windowStore(h windows.HWND) *propertyStore {
	if pPropStoreForWindow.Find() != nil {
		return nil
	}
	var ps *propertyStore
	r, _, _ := pPropStoreForWindow.Call(uintptr(h), uintptr(unsafe.Pointer(&iidPropertyStore)), uintptr(unsafe.Pointer(&ps)))
	if int32(r) < 0 || ps == nil || ps.vtbl == nil {
		return nil
	}
	return ps
}

func (ps *propertyStore) release() {
	syscall.SyscallN(ps.vtbl[psRelease], uintptr(unsafe.Pointer(ps)))
}

// getString lit une propriété texte ("" si elle est absente ou d'un autre
// type).
func (ps *propertyStore) getString(key *propertyKey) string {
	var pv propVariant
	r, _, _ := syscall.SyscallN(ps.vtbl[psGetValue], uintptr(unsafe.Pointer(ps)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&pv)))
	if int32(r) < 0 {
		return ""
	}
	s := ""
	if pv.vt == vtLPWSTR && pv.val != 0 {
		s = windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&pv.val)))
	}
	// La chaîne rendue a été allouée pour D3X : PropVariantClear la libère.
	if pPropVariantClear.Find() == nil {
		pPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	}
	return s
}

// setString pose une propriété texte et l'enregistre. Windows copie la
// valeur : la chaîne de Go n'a besoin de vivre que le temps de l'appel.
func (ps *propertyStore) setString(key *propertyKey, s string) bool {
	buf, err := windows.UTF16FromString(s)
	if err != nil {
		return false
	}
	pv := propVariant{vt: vtLPWSTR, val: uintptr(unsafe.Pointer(&buf[0]))}
	r, _, _ := syscall.SyscallN(ps.vtbl[psSetValue], uintptr(unsafe.Pointer(ps)), uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&pv)))
	runtime.KeepAlive(buf)
	if int32(r) < 0 {
		return false
	}
	r, _, _ = syscall.SyscallN(ps.vtbl[psCommit], uintptr(unsafe.Pointer(ps)))
	return int32(r) >= 0
}

// setIdentity : l'identifiant de D3X d'abord, puis l'icône, la commande et le
// nom de relance, dans l'ordre où Chromium pose les siens. La commande et le
// nom vont ensemble : sans l'un, Windows ignore l'autre.
func (ps *propertyStore) setIdentity(exe string) bool {
	return ps.setString(&keyAppID, appID) &&
		ps.setString(&keyRelaunchIcon, exe+",0") &&
		ps.setString(&keyRelaunchCommand, `"`+exe+`"`) &&
		ps.setString(&keyRelaunchName, "D3X")
}

var (
	enumMu    sync.Mutex
	enumFound []windows.HWND
	// Une seule fonction de rappel pour tout D3X : Go n'en crée qu'un nombre
	// limité par processus, jamais libérées, et la fenêtre est cherchée
	// plusieurs fois par seconde.
	enumCallback = sync.OnceValue(func() uintptr {
		return windows.NewCallback(func(h, _ uintptr) uintptr {
			enumFound = append(enumFound, windows.HWND(h))
			return 1
		})
	})
)

// topWindows : toutes les fenêtres de premier niveau du bureau.
func topWindows() []windows.HWND {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumFound = enumFound[:0]
	_ = windows.EnumWindows(enumCallback(), nil)
	return append([]windows.HWND(nil), enumFound...)
}

// appWindows : les fenêtres visibles du navigateur de D3X.
func appWindows(profile string, owners map[uint32]bool) []windows.HWND {
	var out []windows.HWND
	for _, h := range topWindows() {
		if isAppWindow(h, profile, owners) {
			out = append(out, h)
		}
	}
	return out
}

// isAppWindow : une fenêtre visible du navigateur (classe
// « Chrome_WidgetWin_1 ») dont le processus a été lancé avec le profil de D3X.
// Ce n'est pas le processus lancé par OpenWindow : celui-ci passe la fenêtre au
// navigateur déjà lancé sur ce profil (warmUp) et s'arrête aussitôt, et Edge
// lancé à froid se relance lui-même dans un autre processus. owners garde la
// réponse pour chaque processus déjà examiné.
func isAppWindow(h windows.HWND, profile string, owners map[uint32]bool) bool {
	if !windows.IsWindowVisible(h) {
		return false
	}
	var name [64]uint16
	if n, err := windows.GetClassName(h, &name[0], int32(len(name))); err != nil || windows.UTF16ToString(name[:n]) != "Chrome_WidgetWin_1" {
		return false
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(h, &pid); err != nil || pid == 0 {
		return false
	}
	ours, seen := owners[pid]
	if !seen {
		if cmd, ok := commandLine(pid); ok {
			ours = usesProfile(strings.ToLower(cmd), profile)
		}
		owners[pid] = ours
	}
	return ours
}

// taskbarIdentity donne l'identité de D3X (appID, icône et commande de
// relance) à la fenêtre que OpenWindow vient d'ouvrir, dès qu'elle apparaît.
// Tout échec est ignoré : la fenêtre garde alors l'identité du navigateur.
func taskbarIdentity(profileDir string) {
	// L'icône de la barre des tâches ne vaut jamais un arrêt de D3X.
	defer func() { _ = recover() }()
	// Le chemin du lancement, celui où une mise à jour met la nouvelle version.
	exe, err := update.ExePath()
	profile := strings.ToLower(profileDir)
	if err != nil || exe == "" || profile == "" {
		return
	}
	owners := map[uint32]bool{}
	deadline := time.Now().Add(identityWait)
	var settled time.Time
	for {
		if hs := appWindows(profile, owners); len(hs) > 0 && applyIdentity(hs, exe) && settled.IsZero() {
			settled = time.Now().Add(identitySettle)
		}
		now := time.Now()
		if now.After(deadline) || (!settled.IsZero() && now.After(settled)) {
			return
		}
		time.Sleep(identityPoll)
	}
}

// applyIdentity pose l'identité de D3X sur celles de ces fenêtres qui ne la
// portent pas encore ; vrai si au moins une l'a reçue. Une fenêtre qui la
// porte déjà (ouverte avant celle-ci) n'est pas réécrite.
func applyIdentity(hs []windows.HWND, exe string) bool {
	// Les propriétés de fenêtre passent par COM, initialisé sur ce fil le temps
	// des appels seulement : un fil COM ne reste pas inactif pendant l'attente.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil || err == syscall.Errno(windows.S_FALSE) {
		defer windows.CoUninitialize()
	}
	set := false
	for _, h := range hs {
		// L'icône d'abord, même si la fenêtre a déjà l'identité : le navigateur
		// peut remettre la sienne pendant le chargement de la page.
		setWindowIcon(h)
		ps := windowStore(h)
		if ps == nil {
			continue
		}
		if ps.getString(&keyAppID) != appID && ps.setIdentity(exe) {
			set = true
		}
		ps.release()
	}
	return set
}

var (
	pCreateIconFromResourceEx = windows.NewLazySystemDLL("user32.dll").NewProc("CreateIconFromResourceEx")
	pSendMessageTimeout       = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	pGetSystemMetrics         = windows.NewLazySystemDLL("user32.dll").NewProc("GetSystemMetrics")
)

const (
	wmGetIcon       = 0x007F
	wmSetIcon       = 0x0080
	iconSmall       = 0
	iconBig         = 1
	smCxIcon        = 11
	smCxSmIcon      = 49
	smtoAbortIfHung = 0x0002
)

// windowIcons : les icônes de D3X (grande et petite), tirées de icon.ico,
// créées une fois et gardées tant que D3X tourne (la fenêtre s'en sert).
var windowIcons = sync.OnceValues(func() (uintptr, uintptr) {
	mk := func(metric uintptr, fallback int) uintptr {
		size := fallback
		if r, _, _ := pGetSystemMetrics.Call(metric); r > 0 && r <= 256 {
			size = int(r)
		}
		img := icoImage(Icon, size)
		if len(img) == 0 || pCreateIconFromResourceEx.Find() != nil {
			return 0
		}
		h, _, _ := pCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&img[0])), uintptr(len(img)), 1, 0x00030000, uintptr(size), uintptr(size), 0)
		return h
	}
	return mk(smCxIcon, 32), mk(smCxSmIcon, 16)
})

// setWindowIcon pose l'icône de D3X sur la fenêtre du navigateur. Edge et
// Chrome y mettaient l'icône de la page, mais Brave laissait une icône vide
// dans la barre des tâches (AsTaGoR, 17/11/26). Rien n'est envoyé si la
// fenêtre porte déjà nos icônes ; une fenêtre qui ne répond pas n'attend pas.
func setWindowIcon(h windows.HWND) {
	big, small := windowIcons()
	for _, ic := range []struct{ kind, icon uintptr }{{iconBig, big}, {iconSmall, small}} {
		if ic.icon == 0 {
			continue
		}
		var cur uintptr
		pSendMessageTimeout.Call(uintptr(h), wmGetIcon, ic.kind, 0, smtoAbortIfHung, 200, uintptr(unsafe.Pointer(&cur)))
		if cur == ic.icon {
			continue
		}
		var res uintptr
		pSendMessageTimeout.Call(uintptr(h), wmSetIcon, ic.kind, ic.icon, smtoAbortIfHung, 200, uintptr(unsafe.Pointer(&res)))
	}
}
