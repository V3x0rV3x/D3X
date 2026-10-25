// D3X : l'outil de bureau des membres de V3X.
//
// Il tourne sur ta machine, ouvre sa page dans une fenêtre d'application et
// parle à ton client torrent et à l'API de V3X avec ta clé personnelle.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/V3x0rV3x/D3X/internal/agent"
	"github.com/V3x0rV3x/D3X/internal/config"
	"github.com/V3x0rV3x/D3X/internal/desktop"
	"github.com/V3x0rV3x/D3X/internal/server"
	"github.com/V3x0rV3x/D3X/internal/update"
)

// version est posée à la compilation (-ldflags "-X main.version=1.2.3").
var version = "dev"

// Clé publique qui signe les versions (sa moitié privée n'est pas dans ce
// dépôt).
//
//go:embed d3x-signature.pub
var signingKey []byte

const defaultPort = 8733

// windowCheck : délai laissé à la page pour se manifester avant que D3X tienne
// sa fenêtre pour vide (un navigateur lancé à froid met quelques secondes).
const windowCheck = 15 * time.Second

// hiddenCheck : une page chargée qui ne s'est toujours pas affichée au bout de
// ce délai est dans une fenêtre ratée.
const hiddenCheck = 6 * time.Second

// Variable passée à D3X relancé après une mise à jour : « port:jeton ». Il
// reprend la même adresse et la même session, la page ouverte se reconnecte.
const takeoverEnv = "D3X_TAKEOVER"

func main() {
	// Lancé sur une seedbox par D3X : agent sans page ni fenêtre.
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		if err := agent.Run(version, os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "erreur : "+err.Error())
			os.Exit(1)
		}
		return
	}
	// Docker : vérification de santé (l'image n'a ni curl ni shell).
	if len(os.Args) > 1 && os.Args[1] == "health" {
		os.Exit(health())
	}
	port := flag.Int("port", defaultPort, "port local de la page")
	noBrowser := flag.Bool("no-browser", false, "ne pas ouvrir de fenêtre")
	listen := flag.String("listen", os.Getenv("D3X_LISTEN"), "mode serveur (Docker, NAS) : adresse d'écoute, par exemple 0.0.0.0:8733")
	showVersion := flag.Bool("version", false, "afficher la version")
	flag.Parse()
	if *showVersion {
		fmt.Println("D3X", version)
		return
	}
	setupLog()
	if *listen != "" {
		if err := serve(*listen); err != nil {
			log.Println("arrêt :", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*port, !*noBrowser); err != nil {
		log.Println("arrêt :", err)
		desktop.MessageBox("D3X", "D3X n'a pas pu démarrer : "+err.Error())
		os.Exit(1)
	}
}

// setupLog écrit le journal dans le dossier de D3X (sous Windows, il n'y a pas
// de console), et aussi dans le terminal s'il y en a un.
func setupLog() {
	p, err := config.Path("d3x.log")
	if err != nil {
		return
	}
	if st, err := os.Stat(p); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(f, os.Stdout))
	log.SetFlags(log.LstdFlags)
}

func run(port int, window bool) error {
	update.Cleanup()
	takeover := os.Getenv(takeoverEnv)
	_ = os.Unsetenv(takeoverEnv)

	// Une mise à jour vérifiée attend depuis la dernière fois : on l'installe et
	// on relance la nouvelle version avant toute chose.
	if takeover == "" {
		if swapped, err := update.Swap(); err != nil && !errors.Is(err, update.ErrNoStaged) {
			log.Println("mise à jour non installée :", err)
		} else if swapped {
			log.Println("mise à jour installée, relance")
			return relaunch("")
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration illisible : %w", err)
	}

	// Déjà lancé ? On rouvre simplement sa fenêtre.
	if takeover == "" {
		if url, ok := runningInstance(); ok {
			log.Println("D3X tourne déjà, ouverture de sa fenêtre")
			if window {
				openWindow(url)
			}
			return nil
		}
	}

	token := server.NewToken()
	var ln net.Listener
	if takeover != "" {
		// Relancé après une mise à jour : même port, même jeton. L'ancien D3X
		// libère le port en s'arrêtant, on attend qu'il soit libre.
		parts := strings.SplitN(takeover, ":", 2)
		if len(parts) == 2 {
			port, _ = strconv.Atoi(parts[0])
			token = parts[1]
		}
		for i := 0; i < 50; i++ {
			if ln, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			return err
		}
	} else {
		// Fenêtre fantôme d'un D3X précédent (rectangle blanc ou gris resté sur
		// le bureau) : on la ferme avant d'en ouvrir une neuve.
		if window {
			if n := desktop.CloseWindow(profileDir()); n > 0 {
				log.Printf("ancienne fenêtre de D3X fermée (%d processus)", n)
			}
		}
		if ln, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err != nil {
			if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
				return err
			}
		}
	}
	port = ln.Addr().(*net.TCPAddr).Port
	_ = config.WriteFileAtomic("session", []byte(strconv.Itoa(port)+" "+token))

	srv := server.New(version, cfg, token, port)
	if pub, err := update.PublicKey(signingKey); err == nil {
		srv.SetUpdateKey(pub)
	}
	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/auth?t=%s", port, token)
	log.Printf("D3X %s tourne sur http://127.0.0.1:%d", version, port)

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	var quitOnce sync.Once
	quit := func(code int) {
		quitOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(ctx)
			removeSession()
			desktop.QuitTray()
			os.Exit(code)
		})
	}
	// Arrêt de D3X (icône « Quitter », fenêtre fermée, signal, serveur tombé) :
	// sa fenêtre part avec lui. Jamais pendant une mise à jour : la nouvelle
	// version reprend la même fenêtre.
	stop := func(code int) {
		if window {
			desktop.CloseWindow(profileDir())
		}
		quit(code)
	}

	// Fenêtre : la page signale sa présence (signal régulier, « au revoir » à la
	// fermeture). Fenêtre fermée hors travail : D3X s'arrête ; pendant un
	// travail, il reste dans la zone de notification et s'arrête une fois fini.
	// (Surveiller le processus du navigateur ne marche pas : Edge passe la
	// fenêtre à un autre de ses processus et rend la main tout de suite.)
	// showWindow dit si la page est partie dans la fenêtre d'application (et
	// non dans le navigateur par défaut).
	showWindow := func() bool {
		if desktop.OpenWindow(url, profileDir()) != nil {
			return true
		}
		if !desktop.HasTray {
			fmt.Println("Ouvre ce lien dans ton navigateur :", url)
		}
		_ = desktop.OpenDefault(url)
		return false
	}
	// Lancement avec Windows : l'entrée suit l'exécutable s'il a changé de place.
	if desktop.HasAutostart && desktop.Autostart() {
		_ = desktop.SetAutostart(true)
	}
	if desktop.HasTray && window {
		go func() {
			cleaned := false
			// Heure murale, comme PageGone (l'horloge monotone peut ne pas compter
			// la veille) : un tour de 5 s qui en a duré bien plus, c'est un réveil.
			tick := time.Now().Round(0)
			for {
				time.Sleep(5 * time.Second)
				now := time.Now().Round(0)
				if now.Sub(tick) > 30*time.Second {
					log.Println("sortie de veille : la page a le temps de revenir")
					srv.Resumed()
				}
				tick = now
				// Lot en cours : Windows ne met pas le PC en veille de lui-même.
				// Un lot en attente d'une page pour le MediaInfo n'avance plus :
				// la veille revient.
				if on := srv.LotAdvancing(); desktop.KeepAwake(on) {
					if on {
						log.Println("lot en cours : mise en veille automatique retenue")
					} else {
						log.Println("plus de lot en cours : mise en veille automatique rendue")
					}
				}
				if !srv.PageGone() {
					cleaned = false
					continue
				}
				if !srv.Busy() && !srv.StayInTray() {
					log.Println("fenêtre fermée, rien en cours : arrêt (" + srv.GoneWhy() + ")")
					stop(0)
				}
				// D3X reste dans la zone de notification : la fenêtre fermée ne
				// doit pas laisser de fantôme derrière elle.
				if !cleaned && srv.PageClosed() {
					desktop.CloseWindow(profileDir())
					cleaned = true
				}
			}
		}()
	}

	// Mise à jour demandée par la page : nouveau binaire, relance sur la même
	// session, arrêt de celui-ci.
	go func() {
		for {
			<-srv.RestartRequested()
			if _, err := update.Swap(); err != nil {
				log.Println("mise à jour impossible :", err)
				srv.RestartFailed(err)
				continue
			}
			break
		}
		_ = httpSrv.Close() // libère le port pour la nouvelle version
		if err := relaunch(strconv.Itoa(port) + ":" + token); err != nil {
			log.Println("relance impossible :", err)
		}
		quit(0)
	}()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		select {
		case <-sig:
			stop(0)
		case err := <-errc:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Println("serveur :", err)
				stop(1)
			}
		}
	}()

	if takeover == "" && window {
		go func() {
			time.Sleep(300 * time.Millisecond)
			if !showWindow() || !desktop.HasTray {
				return
			}
			// Fenêtre ratée (Luci, 21/10/26 : il fallait lancer D3X deux fois),
			// une seule reprise :
			//   • page chargée mais jamais à l'écran : une seconde fenêtre dans le
			//     navigateur lancé, comme le second lancement qui marchait chez
			//     elle ; la première se ferme d'elle-même (ping « close ») ;
			//   • page jamais venue : la fenêtre est fermée et rouverte.
			for waited := time.Duration(0); waited < windowCheck; waited += 500 * time.Millisecond {
				time.Sleep(500 * time.Millisecond)
				if srv.PageShown() {
					return
				}
				if waited >= hiddenCheck && srv.PageHidden() {
					log.Println("fenêtre ouverte mais pas affichée, nouvelle fenêtre")
					showWindow()
					return
				}
			}
			if srv.PageSeen() {
				return
			}
			log.Println("fenêtre restée vide, réouverture")
			desktop.CloseWindow(profileDir())
			showWindow()
		}()
	} else if !window {
		fmt.Println("Ouvre ce lien dans ton navigateur :", url)
	}

	if desktop.HasTray {
		desktop.Tray(func() {}, func() { showWindow() }, func() {
			srv.StopLot() // un lot s'arrête après la release en cours
			if srv.Busy() {
				desktop.MessageBox("D3X", "D3X termine d'abord ce qu'il a en cours (ajouts, vérifications ou publication), puis s'arrêtera.")
				go func() {
					for srv.Busy() {
						time.Sleep(5 * time.Second)
					}
					stop(0)
				}()
				return
			}
			stop(0)
		})
		return nil
	}
	fmt.Println("Ferme cette fenêtre (ou Ctrl+C) pour arrêter D3X.")
	select {}
}

// serve : mode serveur (Docker, NAS). La page est ouverte depuis une autre
// machine avec un lien à jeton, stable d'un lancement à l'autre (rangé dans le
// dossier de D3X, ou donné par D3X_TOKEN). Ni fenêtre, ni mise à jour par
// lui-même quand D3X_MANAGED est posé (image Docker : on met l'image à jour).
func serve(addr string) error {
	// Restes d'une mise à jour (binaire « .old »), comme au lancement en fenêtre.
	update.Cleanup()
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration illisible : %w", err)
	}
	token, err := serverToken()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := server.NewWith(version, cfg, token, port, server.Options{Remote: true, Managed: os.Getenv("D3X_MANAGED")})
	if pub, err := update.PublicKey(signingKey); err == nil {
		srv.SetUpdateKey(pub)
	}
	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("D3X %s en mode serveur sur %s", version, ln.Addr())
	log.Printf("Ouvre http://ADRESSE-DE-CETTE-MACHINE:%d/auth?t=%s", port, token)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	// Mise à jour installée depuis la page (binaire seul, hors Docker) : même
	// adresse et même jeton, la page se reconnecte.
	restarted := make(chan struct{})
	go func() {
		for {
			<-srv.RestartRequested()
			if _, err := update.Swap(); err != nil {
				log.Println("mise à jour impossible :", err)
				srv.RestartFailed(err)
				continue
			}
			break
		}
		_ = httpSrv.Close()
		if err := relaunch(""); err != nil {
			log.Println("relance impossible :", err)
		}
		close(restarted)
	}()
	select {
	case <-restarted:
		return nil
	case <-stop:
		// docker stop : le lot s'arrête après la release en cours, et on laisse
		// finir ce qui est en route (dans la limite du délai de Docker), sans
		// couper un ajout au milieu.
		srv.StopLot()
		for i := 0; i < 8 && srv.Busy(); i++ {
			time.Sleep(time.Second)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return httpSrv.Shutdown(ctx)
	case err := <-errc:
		// Serveur fermé pour la mise à jour : on attend la relance.
		if errors.Is(err, http.ErrServerClosed) {
			select {
			case <-restarted:
			case <-time.After(15 * time.Second):
			}
			return nil
		}
		return err
	}
}

// serverToken : D3X_TOKEN, sinon le jeton rangé (créé au premier lancement).
func serverToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("D3X_TOKEN")); t != "" {
		if len(t) < 24 {
			return "", errors.New("D3X_TOKEN trop court (24 caractères au moins)")
		}
		return t, nil
	}
	p, err := config.Path("token")
	if err != nil {
		return "", err
	}
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 24 {
		return strings.TrimSpace(string(b)), nil
	}
	t := server.NewToken()
	if err := config.WriteFileAtomic("token", []byte(t)); err != nil {
		return "", err
	}
	return t, nil
}

// health interroge le D3X local (HEALTHCHECK de l'image Docker).
func health() int {
	port := strconv.Itoa(defaultPort)
	if a := os.Getenv("D3X_LISTEN"); a != "" {
		if _, p, err := net.SplitHostPort(a); err == nil && p != "" && p != "0" {
			port = p
		}
	}
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
	if string(b) != "D3X" {
		return 1
	}
	return 0
}

// relaunch démarre le binaire (éventuellement nouveau) avec les mêmes options.
func relaunch(takeover string) error {
	// Le chemin du lancement, pas os.Executable : sous Linux, celui-ci suit le
	// binaire renommé en « .old » et relancerait l'ancienne version.
	exe, err := update.ExePath()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = os.Environ()
	if takeover != "" {
		cmd.Env = append(cmd.Env, takeoverEnv+"="+takeover)
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Start()
}

func profileDir() string {
	d, err := config.Dir()
	if err != nil {
		return filepath.Join(os.TempDir(), "d3x-window")
	}
	return filepath.Join(d, "fenetre")
}

// openWindow : la fenêtre d'un D3X qui tourne déjà, ouverte par ce lancement
// (relancé depuis son épingle, par exemple), qui s'arrête aussitôt après. Il
// attend d'abord que la fenêtre porte l'identité de D3X dans la barre des
// tâches : arrêté avant, elle garderait celle du navigateur.
func openWindow(url string) {
	w := desktop.OpenWindow(url, profileDir())
	if w == nil {
		_ = desktop.OpenDefault(url)
		return
	}
	w.WaitIdentity()
}

// runningInstance lit la session rangée et vérifie qu'un D3X y répond.
func runningInstance() (string, bool) {
	p, err := config.Path("session")
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return "", false
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + f[0] + "/health")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
	if string(body) != "D3X" {
		return "", false
	}
	return "http://127.0.0.1:" + f[0] + "/auth?t=" + f[1], true
}

func removeSession() {
	if p, err := config.Path("session"); err == nil {
		_ = os.Remove(p)
	}
}
