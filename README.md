# D3X

L'outil de bureau des membres de [V3X](https://v3x.club).

![Cross-seed avec D3X : le client est analysé, trois releases cochées sont ajoutées, vérifiées, puis mises en seed ; celle dont les fichiers diffèrent reste à l'arrêt](docs/images/cross-seed.gif)

- **Cross-seed** : D3X regarde ce que ton client torrent seede déjà pour d'autres trackers, retrouve sur V3X les releases identiques et les ajoute sur **les mêmes fichiers**. Tu seedes pour V3X sans rien retélécharger ni dupliquer sur ton disque. Les releases mortes et les relais primés passent en tête ; celles qui manquent à V3X se publient d'un clic, et celles qu'un membre demande sont signalées avec leur prime.
- **Médiathèque** : tes films et séries rangés et renommés, sans leur .torrent, retrouvés sur V3X et vérifiés morceau par morceau sur ton disque.
- **Mon seed** : ce que V3X attend de ton seed (hit and run à éviter, relais à reprendre) comparé à ce que fait vraiment ton client.
- **Surveillance** : D3X relance l'analyse tout seul, te prévient des nouvelles releases à cross-seeder et d'un hit and run en vue, et peut ajouter seul les releases identiques.
- **Publier** : tu choisis un dossier ou un fichier, sur ce PC ou sur ta seedbox, D3X fabrique le .torrent là où sont les fichiers pendant que tu remplis la fiche, que V3X prépare exactement comme sur sa page d'upload. Une fois publiée, la release part en seed dans ton client, sur les mêmes fichiers.
- **Musique** : D3X lit les tags et la qualité des pistes, retrouve l'album dans MusicBrainz en comparant les durées, et prépare la fiche album du site.
- **Jeux vidéo** : dans Jeux vidéo ou Émulation, tu cherches le jeu par son titre ; titre, jaquette et fiche jeu du site (genres, studios, config requise) se remplissent, comme sur la page d'upload.
- **Livres** : dans Ebook (ou pour une release faite de .epub, .pdf…), le lien Open Library du livre ou son ISBN remplit titre, auteur, couverture et fiche livre, comme sur la page d'upload. Il est facultatif : un livre introuvable (sortie récente, petite édition) se publie sans, en choisissant soi-même la sous-catégorie.
- **Lot** : un dossier entier passé au crible. D3X ne publie seul que ce qui est sûr ; le reste attend ta revue.

Clients pris en charge : **qBittorrent**, **Transmission**, **Deluge** et **rTorrent** (ruTorrent), sur ce PC, un NAS ou une seedbox, et [plusieurs à la fois](#plusieurs-clients) (une seedbox et ce PC, par exemple).

## Ce que fait D3X, et ce qu'il ne fait jamais

- **Il ne modifie ni ne supprime aucun de tes fichiers.** Pour publier, il lit tes fichiers (hachage, MediaInfo) ; pour le reste, tout passe par l'interface web de ton client. Sur une seedbox, il n'écrit que son agent dans `~/.d3x` et son cache de hachage dans `~/.config/D3X`. Seule exception, si tu l'actives : les [liens](#les-liens) (physiques ou symboliques), créés dans un dossier que tu choisis, sans jamais rien écraser.
- **Il ne télécharge rien à ta place.** Chaque release est ajoutée *à l'arrêt*, ton client vérifie les données, et D3X ne la démarre que si elle est complète à 100 %. Sinon elle reste arrêtée, avec un bouton pour la retirer (fichiers intacts ; avec rTorrent, D3X l'arrête et te laisse la retirer, car rTorrent peut être réglé pour effacer les données au retrait). Une release sortie de la file d'attente de vérification sans avoir été vérifiée (client redémarré pendant l'attente) est revérifiée une fois avant d'être dite différente.
- **Cross-seed : il n'envoie à V3X que des empreintes.** Pour chaque torrent de ton client, D3X calcule chez toi deux empreintes (SHA-256 des chemins et des tailles, puis des tailles seules). Aucun nom de fichier, aucun chemin, aucun info-hash d'un autre tracker ne quitte ta machine.
- **Correspondance exacte seulement.** Une release n'est proposée que si ses fichiers ont exactement les mêmes tailles que sur V3X, fichier par fichier, et les mêmes noms (sinon elle va dans « Noms différents »), jamais sur la seule taille totale ni sur une ressemblance de nom. Pour une petite release (moins de 100 Mio), un nom qui ne diffère que par les majuscules ou la ponctuation passe aussi en « Noms différents ». Le .torrent reçu de V3X est revérifié avant l'ajout, et le client vérifie chaque octet avant tout démarrage.
- **Médiathèque : seules les tailles des gros fichiers partent vers V3X** (plus de 50 Mio), jamais un nom ni un chemin. Les .torrent des releases trouvées sont ensuite comparés à tes fichiers chez toi.
- **Musique : seuls l'artiste et le titre de l'album partent vers [MusicBrainz](https://musicbrainz.org)** (base ouverte de la musique), interrogée directement, une requête par seconde comme elle le demande.
- **Publier : il envoie ce que tu enverrais sur le site**, le .torrent et la fiche (nom, catégorie, présentation, NFO avec le MediaInfo, sans aucun chemin local). Tes fichiers ne sont jamais envoyés à V3X.

Le code est ouvert : tu peux vérifier tout ce qui précède avant de lancer l'outil. Les appels au site sont tous dans [`internal/v3x`](internal/v3x).

## Cross-seed

L'analyse range tes releases en onglets :

- **À ajouter** : identiques sur V3X. Les **relais** passent en tête (releases rares lâchées par un membre : une prime en jetons aux premiers qui la reprennent, après 24 h de seed), puis les **releases mortes** (plus aucun seeder depuis trois jours : ton seed les ramène, et la prime de résurrection tombe une fois qu'elles ont servi).
- **Noms différents** : les mêmes fichiers sous d'autres noms (voir les [questions](#questions)).
- **Pas sur V3X** : ce que tu seedes ailleurs et qui manque à V3X. « Publier » ouvre la fiche d'une release, ou coches-en plusieurs pour les passer au crible du lot. Celles qui répondent à une **demande ouverte** sur le site sont en tête, avec la prime ; une fois ta release publiée et validée, D3X la propose au demandeur d'un clic. Une release seedée plusieurs fois (un torrent par tracker, sur des liens physiques) n'y apparaît qu'une fois, avec son nombre de copies (×2, ×3…). Celles d'une team qui interdit le réupload, ou le soumet à son accord, portent une pastille qui le dit, comme celles d'un **groupe refusé sur V3X** (avec la raison donnée par le site), et une case masque celles que V3X refusera. Le tag est reconnu aussi séparé par un espace (`… x264 ac3 mHDgz.mkv`). Une copie sans tag, ou renommée, de la même taille au fichier près qu'une copie taguée que tu as (au-delà de 100 Mio) porte la pastille « Detag » de cette team : un detag d'une team qui interdit le réupload ne se publie pas, D3X ne l'ouvre pas dans Publier et ne la passe pas au lot.
- **Autres** :
  - **Même vidéo sur V3X** : le plus gros fichier de ta copie, une vidéo de 200 Mio ou plus, existe à l'octet près dans une fiche V3X, sous une autre forme (nom refait, .nfo en plus, dossier au lieu d'un fichier seul). Rien ne manque au site, rien à publier. Pour une image disque (`.iso`, souvent un jeu), la taille ne suffit pas : il faut aussi le même nom de release, ou une image au même nom de fichier que celle de la fiche, et la ligne devient **Même fichier sur V3X**.
  - **Absente du disque** : le client croit la copie complète, mais ses fichiers ne sont plus là (supprimés, ou déplacés par un outil de médiathèque). D3X ne l'ajoute pas.
  - **Retirée de V3X** : aucune fiche en ligne, mais V3X a retiré (ou remplacé, archivé) une fiche de ce contenu. D3X ne la propose pas à la publication ; le motif s'affiche si c'était ta fiche. Publier reste possible si la raison ne s'applique plus.

Au-dessus de la liste, **Filtrer** ne garde que les releases dont le nom contient tous tes mots ; un mot précédé de « - » les écarte (`1080p MULTi -720p`, ou le tag d'une team). « Tout cocher » ne prend que ce qui est affiché.

Une release classée d'une façon qui te surprend : **Diagnostic** affiche ce que D3X en sait (nom, fichiers et tailles, ce qu'il en a conclu), sans info-hash ni chemin sur ton disque, prêt à copier pour le staff.

Une release que tu ne veux pas seeder pour l'instant : **Ignorer** la retire de la liste et de « Tout cocher », et elle ne revient plus à chaque analyse. Ça marche dans « À ajouter », « Noms différents » et « Pas sur V3X » ; « Les réafficher » la retrouve. La liste des releases ignorées reste sur l'appareil.

D3X retrouve les fichiers d'une release sur ce PC, sur ta seedbox, ou sur un NAS si tu lui indiques où il voit les dossiers du client (Réglages ▸ Clients torrent ▸ Dossiers du client).

## Plusieurs clients

Tes films sur une seedbox dans Deluge, tes livres sur ce PC dans qBittorrent ? Règle les deux dans Réglages ▸ Clients torrent (huit au plus), chacun avec son nom (« Seedbox », « PC »), son adresse, et ses propres dossiers du client.

- **Analyse** : D3X interroge tous les clients en même temps et fusionne ce qu'ils seedent. Chaque ligne porte le nom de son client.
- **Chaque cross-seed part dans le client qui a déjà les fichiers**, avec le chemin que ce client connaît. Si deux clients ont les mêmes fichiers, une seule ligne, ajoutée au premier de la liste (« Monter » change l'ordre). Une release déjà présente dans l'un des clients n'est jamais proposée.
- **Un client injoignable ne bloque pas les autres** : un bandeau le signale (« Seedbox injoignable : … ») et l'analyse continue sans lui. Il est retenté au début de chaque analyse.
- **Mon seed et la surveillance** regardent tous les clients : une release en seed dans l'un d'eux est en seed. Si un client ne répond pas, ce qu'il seede n'est pas compté comme absent, et aucune alerte de hit and run n'en découle.
- **Publier** : la release part en seed dans le client dont les dossiers couvrent celui de la release, sinon celui qui seede déjà dans ce dossier, sinon celui qui tourne sur la machine des fichiers, sinon le premier. Tu peux en choisir un autre jusqu'à l'envoi.
- **Médiathèque** : tu choisis le client qui reçoit ses ajouts (le premier par défaut).
- **Liens** : un dossier de liens par client, tel que ce client le voit.

Avec un seul client, rien ne change : mêmes écrans, aucune étiquette.

## Mon seed

- **Hit and run** : chaque release à seeder encore, le temps fait et l'échéance, comparés à l'état réel dans ton client (en seed, arrêtée, absente). Une release absente de ce D3X mais que V3X voit en seed chez toi (une autre machine, un autre D3X) est marquée « En seed ailleurs », pas « Absente du client ». Celles qui n'auront plus le temps d'y arriver passent en rouge, en tête.
- **Relais** : ceux que tu as déjà, ou que tu peux ajouter en cross-seed.
- **Releases V3X arrêtées** : complètes dans ton client mais arrêtées, elles ne seedent pas. Un bouton les redémarre (seulement les complètes : D3X ne lance jamais un téléchargement).

## Surveillance

Dans Réglages ▸ Surveillance, D3X relance l'analyse à intervalle régulier : toutes les 15 ou 30 minutes, toutes les heures, toutes les 3, 6 ou 12 heures, ou une fois par jour. Une analyse part aussi au lancement de D3X. Seuls les torrents arrivés depuis la dernière fois sont relus : ton client n'est presque pas sollicité.

- **Notification** sur ton bureau pour les nouvelles releases à cross-seeder et pour un hit and run en vue.
- **Ajout automatique**, si tu le coches : seulement les nouvelles releases identiques (mêmes fichiers, mêmes noms), jamais les « noms différents », avec la règle habituelle (à l'arrêt, vérifiées, démarrées à 100 %). Le premier passage ne fait que noter l'existant : ce qui était déjà là s'ajoute à la main.
- Fenêtre fermée, D3X reste dans la zone de notification. Sous Windows, il peut aussi se lancer avec la session, sans ouvrir sa fenêtre.
- Sous Windows, la fenêtre de D3X s'épingle dans la barre des tâches avec l'icône de D3X, et l'épingle relance D3X. Une épingle faite avant la 1.1.21 garde l'icône d'Edge : retire-la et épingle de nouveau.

## La médiathèque

Une médiathèque renomme les fichiers et ne garde pas le .torrent : D3X les retrouve quand même.

1. Indique tes dossiers de films et de séries, sur ce PC ou sur ta seedbox.
2. D3X liste les fichiers et envoie à V3X **leurs tailles seules** (les gros fichiers). V3X répond par les releases qui ont un fichier de cette taille.
3. Pour chaque candidate, D3X lit son .torrent et cherche **chacun de ses fichiers** dans ta médiathèque, puis lit quelques morceaux de chaque fichier sur ton disque et compare leur empreinte à celle du .torrent. Une taille identique ne suffit jamais.
4. Tu coches ce que tu veux seeder. L'ajout se fait par [liens](#les-liens) si tu les as activés (ta médiathèque n'est jamais touchée), sinon en renommant dans ton client, comme pour les « noms différents ».
5. Une release à qui il ne manque que des petits fichiers annexes (.nfo, image, texte, sous-titre) est **À compléter** (onglet Incomplètes) : avec le dossier de liens et des liens physiques, ton client vérifie ce qui est là, télécharge les morceaux qui contiennent ces fichiers (quelques Mio au plus), puis seede.

Une release dont il manque un fichier (souvent un petit .nfo) est signalée « incomplète » : ton client ne pourrait pas la vérifier à 100 %, elle n'est pas proposée.

![La médiathèque : deux films renommés retrouvés sur V3X, vérifiés morceau par morceau sur le disque, prêts à être ajoutés](docs/images/mediatheque.png)

## Les liens

Certains clients ne savent pas renommer les fichiers d'un torrent (rTorrent), et D3X refuse de renommer quand un nom pourrait déjà exister. Dans Réglages ▸ Liens, choisis un **dossier de liens**, tel que ton client le voit (un par client si tu en as plusieurs) : D3X y range la release V3X, chacun de ses fichiers étant un lien physique vers le fichier de ta copie.

- Aucune place en plus : un lien physique est le même fichier sous un autre nom.
- Ta copie n'est pas touchée, et rien n'est jamais écrasé : un lien ne se crée que là où il n'y a rien. Si l'ajout échoue, les liens sont retirés, et D3X n'efface un lien que s'il pointe bien vers ta copie.
- Le dossier doit exister, sur le même disque que tes fichiers, et D3X doit le voir : sur ce PC, par un partage avec les dossiers du client, ou sur ta seedbox par son agent. Sans dossier des liens, D3X n'en fait aucun pour ce client et repasse par le renommage : les Réglages le signalent.
- **Liens symboliques** (case à cocher au même endroit, qui active aussi les liens) : pour une copie qu'on ne peut pas lier physiquement, comme un partage NFS ou SMB en lecture seule ou un autre disque. Chaque lien vise le fichier tel que ton client le voit, et le dossier des liens peut être n'importe où, tant que ton client peut y écrire. Ne déplace pas ta copie ensuite : le lien pointerait dans le vide. Sous Windows, il faut le mode développeur.

## Publier, en mieux

- **Doublon repéré avant de hacher** : l'empreinte des noms et tailles suffit pour savoir si la release existe déjà sur V3X, sans passer dix minutes à hacher 50 Gio pour rien. Si elle y est à l'identique, « Seeder la release V3X avec ces fichiers » l'ajoute à ton client à l'arrêt, la fait vérifier et la démarre à 100 %, sans rien publier.
- **Le nom de release, même quand le dossier n'en porte pas** : un dossier rangé par une médiathèque (`1917 (2019)`) n'a pas le nom de la release. Si sa vidéo principale en a un (`1917.2019.MULTi.1080p.BluRay.x264-GROUPE.mkv`), c'est lui qui est proposé, sans l'extension. Un dossier qui porte déjà un nom de release (tag de groupe, résolution, source ou codec) garde le sien.
- **Déjà sur V3X** : les versions du même film, du même épisode ou du même livre déjà en ligne, avec leur taille ; celle qui a presque la même taille que la tienne est signalée, et un REPACK (ou PROPER) de ta release passe en tête. Dans le Lot, la version d'origine d'une release dont le REPACK est en ligne attend dans « À vérifier ». Rien n'est bloqué dans Publier : c'est toi qui décides. Si tu ne publieras pas ta version, « Ignorer cette release » l'écarte du Lot, et de « Pas sur V3X » si tu l'as ouverte depuis là, sans repasser par le cross-seed.
- **Hachage sur tous les cœurs**, mémoire bornée, et un cache : une release déjà hachée ne l'est plus jamais.
- **MediaInfo sans rien installer** : la même bibliothèque que la page d'upload du site, calculée dans la page de D3X.
- **La fiche est celle du site** : nom suggéré, catégorie, TMDB, NFO et présentation viennent du même générateur que la page d'upload de V3X. Éditeur avec barre d'outils et aperçu en direct.
- **Ton propre .nfo** : avec l'option « Reprendre le .nfo de la release » (sous le NFO, gardée sur l'appareil), le .nfo du dossier de la release (NFO de team, source…) remplace celui de V3X, avec le MediaInfo ajouté à la suite s'il n'en contient pas. Elle vaut aussi pour le Lot.
- **Une série choisie une fois** : la série TMDB que tu choisis à la main pour une saison est reprise pour les autres saisons de la même série, dans Publier comme dans le Lot.
- **Présentation en HTML** pour les membres que V3X y autorise (même règle que le site) : choix BBCode ou HTML au-dessus de l'éditeur, retenu sur l'appareil, et aperçu tel que le site l'affichera.
- **Rapport VirusTotal** demandé pour les catégories où V3X l'exige (applications, jeux), compté dans « Il manque » tant qu'il n'est pas rempli, avec le lien vers le site d'analyse. Vingt rapports au plus, un par ligne.
- **Image du PC** : un bouton de la barre d'outils envoie une image de ton ordinateur (pochette, capture), hébergée sur V3X avec ta clé, et l'insère dans la présentation.
- **Charger un fichier** : remplace la présentation par celle d'un fichier, un .txt en BBCode, ou un .html si V3X t'autorise la présentation en HTML.
- **Pochette Discogs** : pour un album sans pochette sur MusicBrainz, avec ton jeton Discogs (Réglages ; il se crée sur discogs.com ▸ Paramètres ▸ Développeurs). Tu choisis l'édition, D3X récupère sa pochette en grand, l'héberge sur V3X et la place en tête de la présentation.
- **Seed automatique**, même quand ton client tourne sur un NAS ou dans Docker : D3X devine où le client voit tes fichiers, d'après les torrents qu'il seede déjà.
- Fichiers cachés et aperçus de médiathèque écartés (`.DS_Store`, `*.trickplay`, `extrafanart`…), liens symboliques suivis sans boucle, fichier encore en cours d'écriture détecté.

![La fiche d'un film dans Publier : l'éditeur de présentation et l'aperçu tel que le site l'affichera](docs/images/publier-apercu.png)

## La musique

Un dossier d'album (une ou plusieurs pistes, plusieurs disques compris) est reconnu tout seul. Avec une vidéo dedans (un clip, un live), c'est le poids qui tranche : un album si ses pistes pèsent plus lourd que ses vidéos (une discographie avec un clip), une vidéo sinon (un film livré avec sa bande originale). Le Lot suit la même règle. Si D3X se trompe, un bouton de Publier bascule la release en album ou en vidéo.

![Un album dans Publier : l'édition MusicBrainz retrouvée d'après les durées des pistes, Audio ▸ Musique, et la qualité proposée pour le nom](docs/images/musique.png)

- **Tags et qualité lus sur place**, sur ce PC ou sur la seedbox : FLAC, MP3, M4A (AAC, ALAC), Ogg Vorbis, Opus, WAV et DSF. Seuls les en-têtes sont lus.
- **L'album retrouvé dans MusicBrainz** : chaque édition est comparée aux pistes, leur nombre puis leurs durées à deux secondes près. L'édition désignée par les tags (Picard, beets…) est reprise telle quelle. Entre un CD et une édition numérique aux mêmes pistes, le support noté dans les tags l'emporte.
- **La fiche album du site** (jaquette, tracklist, label), dans Audio ▸ Musique.
- **Le nom** : V3X exige la qualité dans le nom d'un album (`FLAC 24bit-96kHz`, `MP3 320`). D3X la lit dans les fichiers et propose de l'ajouter d'un clic.

Le lot ne publie seul un album que si l'édition est certaine (tags, ou mêmes pistes aux mêmes durées, d'une seule œuvre) et que toutes les pistes ont la même qualité. Un nom sans la qualité part « à vérifier » avec le nom corrigé, ou est complété si l'option du lot est cochée.

## Depuis une seedbox (SSH)

Tes releases sont sur une seedbox ? Pas besoin de les rapatrier : dans Réglages ▸ Seedbox (SSH), indique l'adresse, ton utilisateur et ton mot de passe (ou ta clé SSH). Publier et Lot proposent alors « Ce PC » ou ta seedbox. Plusieurs seedboxes ? Chacune reste enregistrée (sans mot de passe) : choisis-la dans « Seedboxes enregistrées », puis « Se connecter ».

- D3X y dépose son **agent** (un petit programme Linux de 1,4 Mio, compilé avec chaque version, dans `~/.d3x`) qui liste, hache et lit les fichiers **sur place**. Seuls la liste des fichiers, le .torrent et les morceaux lus par MediaInfo reviennent sur ton PC, par la connexion SSH chiffrée.
- L'agent est vérifié par son empreinte SHA-256 avant chaque usage ; il est remplacé à la version suivante de D3X.
- **Clé d'hôte vérifiée** : à la première connexion, D3X te montre l'empreinte de la seedbox et attend ton accord. Si elle change ensuite, tout est bloqué jusqu'à ce que tu acceptes la nouvelle.
- **Le mot de passe n'est jamais enregistré** : il reste en mémoire le temps de la session. Avec une clé SSH sans phrase de passe, la seedbox se reconnecte seule au lancement.
- Aucun nom de fichier ne passe par le shell de la seedbox : chaque demande à l'agent part en un seul argument encodé.
- Le client torrent de la seedbox doit être dans tes clients (Réglages ▸ Clients torrent) : la release publiée part en seed sur les mêmes fichiers.

Seedbox sous Linux x86_64 ou arm64.

## Le lot

Chaque sous-dossier ou fichier d'un dossier est une release. D3X la publie seule **seulement** si l'œuvre TMDB est trouvée avec certitude (même titre, même année à un an près, bon type), que la catégorie est connue, que le MediaInfo est lu et que V3X n'annonce aucun refus. Jamais pour un exécutable, une collection sans fichier à sa racine ou une release au-delà du plafond de taille. Le reste part « à vérifier », avec la raison, et s'ouvre dans Publier d'un clic.

Les **règles de réupload de V3X** valent pour toute release, d'où qu'elle vienne (dossier parcouru, « Pas sur V3X », fiche gardée, Publier ouvert à la main sur un fichier). Une release d'une team qui interdit le réupload, ou qui le soumet à son accord sans te l'avoir donné, celle d'un groupe refusé sur V3X, et le detag ou le retag de l'une d'elles ne partent jamais. Le detag est reconnu à une copie taguée de même taille au fichier près et de même nombre de fichiers : dans tes clients torrent, dans le même lot, ou à côté d'elle dans le même dossier de ce PC (pas encore sur la seedbox). Le Lot la range dans « à vérifier » avant de la hacher, avec la même pastille que dans « Pas sur V3X » ; Publier l'annonce dès l'ouverture et bloque « Publier » et « Garder pour le lot » ; et D3X refuse la publication de son côté si la page ou le lot ne l'avaient pas vue. Si la team t'a donné son accord depuis, D3X relit ses règles sur V3X avant de refuser.

Simulation par défaut, plafond de publications (en nombre, ou en Gio publiés pour suivre le rythme de ton envoi), pause entre deux publications, arrêt propre. **Le lot tourne dans D3X, pas dans la page.** Sur un NAS ou un serveur (Docker), ferme la page : le lot continue, et la page rouverte montre où il en est. Sur ce PC, le MediaInfo est calculé par la page (comme pour Publier) ; avec [MediaInfo](https://mediaarea.net/fr/MediaInfo) installé (`mediainfo` dans le PATH, ou `D3X_MEDIAINFO` pointant vers lui), D3X le calcule seul. Sans lui, fermer la fenêtre met le lot en pause à la release suivante, puis D3X s'arrête comme d'habitude. Pendant un lot, D3X empêche Windows de mettre l'ordinateur en veille de lui-même (l'écran peut s'éteindre), et il ne s'arrête pas si le navigateur endort sa fenêtre (écran éteint) : le lot reprend quand elle se réveille. Disque lent ? L'option « Attendre la vérification du client » fait vérifier chaque release publiée par ton client avant de passer à la suivante, au lieu d'empiler les vérifications, et « Démarrer le seed à la fin du lot » garde les releases publiées à l'arrêt pendant que le lot hache les suivantes, puis les démarre toutes à la fin. Les décisions sont gardées : un lot interrompu ne repart pas seul, même après un redémarrage du conteneur. Il ne part que d'un clic sur « Lancer le lot » (D3X refuse une demande faite avant son redémarrage), et il reprend alors où il en était : ce qui est publié n'est plus proposé. Une release que V3X refuse parce qu'elle, ou le même contenu, y est déjà passe dans « Déjà présent », comme les doublons repérés avant le hachage, et non dans « Erreur ». Pour recommencer avec d'autres options, « Tout remettre en attente » vide l'onglet ouvert (À vérifier, Simulé, Ignoré, Erreur, Déjà présent) sans rien publier ni effacer.

Une release « à vérifier » s'ouvre dans Publier avec **Revoir** : corrige la fiche, puis publie-la tout de suite, ou **Garder pour le lot**. Elle passe alors dans l'onglet **Prête**, et le prochain lot la publie avec ta fiche, telle quelle, sans rien redécider.

![Un lot en simulation : les releases mises de côté, chacune avec sa raison](docs/images/lot.png)

## Aux couleurs de V3X

Le logo, la barre latérale et les polices du site, pour ne pas être perdu. D3X reprend le fond et la couleur d'accent choisis sur ton profil V3X ; tu peux aussi les changer dans Réglages ▸ Apparence (trois fonds, huit accents).

![D3X dans trois fonds et trois accents : sombre, jour, bleu nuit](docs/images/themes.png)

## Installer

1. Télécharge le fichier de ton système dans les [versions publiées](https://github.com/V3x0rV3x/D3X/releases/latest) :
   - Windows : `d3x_windows_amd64.exe`
   - macOS : `d3x_darwin_arm64` (puces Apple) ou `d3x_darwin_amd64` (Intel)
   - Linux : `d3x_linux_amd64` ou `d3x_linux_arm64`
2. Lance-le. D3X s'ouvre dans sa propre fenêtre ; sous Windows, son icône apparaît aussi près de l'horloge (clic pour rouvrir la fenêtre, clic droit ▸ Quitter). Tout reste sur ta machine (`http://127.0.0.1`). Fermer la fenêtre arrête D3X, sauf s'il a un travail en cours (il le termine d'abord) ou si la surveillance est active (il reste dans la zone de notification).

Les mises à jour s'installent d'un clic depuis D3X, et seulement après deux vérifications : la liste des empreintes doit être signée par la clé de V3X, et le fichier doit avoir l'empreinte annoncée. D3X cherche une nouvelle version toutes les heures ; le bouton **Vérifier**, sous le numéro de version en bas à gauche, cherche tout de suite.

Premier lancement :

- **Windows** affiche parfois « Windows a protégé votre ordinateur » pour un programme qu'il ne connaît pas encore : *Informations complémentaires*, puis *Exécuter quand même*.
- **macOS** : `xattr -d com.apple.quarantine d3x_darwin_*` puis `chmod +x d3x_darwin_*`, ou clic droit ▸ Ouvrir.
- **Linux** : `chmod +x d3x_linux_*`.

### Sur un NAS ou un serveur (Docker)

L'image `ghcr.io/v3x0rv3x/d3x` (amd64 et arm64) contient le même binaire que la version publiée, en mode serveur : pas de fenêtre, la page s'ouvre depuis n'importe quel appareil du réseau.

```yaml
services:
  d3x:
    image: ghcr.io/v3x0rv3x/d3x:latest
    container_name: d3x
    restart: unless-stopped
    ports:
      - "8733:8733"
    volumes:
      - d3x:/config
      - /chemin/vers/tes/medias:/data:ro
volumes:
  d3x:
```

1. `docker compose up -d`, puis `docker compose logs d3x` : le journal donne le lien d'accès, `http://ADRESSE-DE-CETTE-MACHINE:8733/auth?t=…`. **Ouvre D3X par ce lien** : l'adresse seule (`IP:8733`) ne suffit pas, la page le rappelle. Le jeton est gardé dans `/config` (ou choisi avec `D3X_TOKEN`) ; le navigateur s'en souvient 400 jours.
2. Dans Réglages, l'adresse du client torrent est celle de son conteneur (`http://qbittorrent:8080`) ou de la machine, jamais `127.0.0.1`. Si le client est dans le même fichier compose, ajoute `depends_on: [qbittorrent]` au service `d3x` pour qu'il démarre après lui.
3. Tes médias sont montés en lecture seule : D3X n'écrit jamais dedans. S'il n'a pas le droit de les lire, lance-le avec l'utilisateur qui les possède (`user: "1000:1000"`, voir `id -u` et `id -g`). Même chose si tu montes un dossier de ta machine sur `/config` ou pour le dossier de liens de la Médiathèque : D3X doit pouvoir y écrire, sinon il affiche « permission denied » avec la ligne `user:` à ajouter.
4. Mise à jour : `docker compose pull && docker compose up -d`. D3X signale les nouvelles versions mais ne se remplace pas lui-même dans un conteneur. Sur un NAS Synology (Container Manager), « Construire » réutilise l'image `latest` déjà présente sans la retélécharger : ajoute `pull_policy: always` au service `d3x`, ou supprime l'image avant de reconstruire.
5. **Le lot continue page fermée** : l'image contient `mediainfo`, D3X calcule le MediaInfo lui-même sur `/data`. Pour une release sur ta seedbox (SSH), c'est encore la page qui le calcule : le lot attend qu'une page soit ouverte, puis repart. `docker stop` n'attend que quelques secondes : pour laisser finir une grosse publication en cours, ajoute `stop_grace_period: 1m` au service `d3x`.

Le jeton protège la page : ne publie pas le lien, et derrière un proxy https, le cookie passe en `Secure`. Le binaire seul fait pareil avec `d3x -listen 0.0.0.0:8733`.

### Vérifier un téléchargement

Chaque version publie `checksums.txt` (l'empreinte SHA-256 de chaque fichier) et `checksums.txt.sig`, la signature de cette liste par la clé de V3X. La clé publique est dans ce dépôt : [`d3x-signature.pub`](d3x-signature.pub).

1. Vérifie que la liste vient bien de V3X (OpenSSL 3, fourni avec Git sous Windows) :
   ```sh
   openssl pkeyutl -verify -pubin -inkey d3x-signature.pub -rawin -in checksums.txt -sigfile checksums.txt.sig
   ```
   La réponse doit être `Signature Verified Successfully`.
2. Compare l'empreinte de ton fichier avec la sienne dans la liste : `sha256sum d3x_linux_amd64`, ou `certutil -hashfile d3x_windows_amd64.exe SHA256` sous Windows.

## Régler

1. **Ta clé API V3X** : [Réglages ▸ Intégrations](https://v3x.club/settings?tab=integrations) sur le site, avec le droit « Torznab » (cross-seed) et le droit « Upload » (publier).
2. **Ton client torrent**, par son interface web, ou plusieurs (« Ajouter un autre client »). Le bouton *Détecter sur ce PC* trouve ceux qui tournent ici. « Tester la connexion » essaie un client sans rien enregistrer ; s'il ne répond pas, D3X dit quoi vérifier (adresse, port, pare-feu, adresse vue depuis Docker).
3. **Ta seedbox** (facultatif), par SSH, pour publier ce qui s'y trouve.
4. **Les dossiers du client** (facultatif), pour chaque client : s'il tourne ailleurs (NAS, Docker), une ligne par dossier, son chemin ici et le même vu par le client.
5. **Catégorie et étiquette** (facultatif), posées sur chaque torrent ajouté par D3X pour les retrouver dans ton client, et une **catégorie de tes publications** pour ranger à part ce que tu publies avec Publier et le Lot (vide : la même). Dans la catégorie, `{cat}` reprend celle de ta copie d'origine dans qBittorrent : `{cat}.cross` range un cross-seed de « tv » dans « tv.cross » (sans catégorie d'origine : « cross »).

| Client | Adresse habituelle | À activer |
| --- | --- | --- |
| qBittorrent (4.5+) | `http://127.0.0.1:8080` | Outils ▸ Options ▸ Interface Web, puis un nom d'utilisateur et un mot de passe dans « Authentification » (à créer : aucun d'office depuis la 4.6) |
| Transmission | `http://127.0.0.1:9091` | Préférences ▸ Distant |
| Deluge | `http://127.0.0.1:8112` | greffon WebUI (mot de passe seul) |
| rTorrent | `https://ta-seedbox/RPC2` | XML-RPC, avec l'identification de la seedbox |

Seedbox qui demande d'abord une identification du serveur (une fenêtre « identifiez-vous » avant même qBittorrent) : D3X y présente le pseudo et le mot de passe des champs du client. Inutile de les écrire dans l'adresse.

Les réglages restent sur ta machine, lisibles par toi seul :

| Système | Dossier |
| --- | --- |
| Windows | `%AppData%\D3X` |
| macOS | `~/Library/Application Support/D3X` |
| Linux | `~/.config/D3X` |

## Questions

**Un cross-seed compte-t-il comme un téléchargement ?** La release apparaît dans tes téléchargements sur V3X, mais comme tu n'as reçu aucun octet, le hit and run ne s'applique pas aux cross-seeds.

**V3X refuse le .torrent ?** Si ton ratio est sous le plancher ou si tu as trop de hit and run non régularisés, le site bloque les nouveaux .torrent, cross-seed compris. D3X affiche la raison. Tes propres publications ne sont jamais bloquées.

**Et l'onglet « Noms différents » ?** Ce sont les mêmes fichiers que sur V3X sous d'autres noms (dossier renommé, épisodes renommés par ta médiathèque…). D3X ajoute la release V3X à l'arrêt, donne à ses fichiers les noms de ta copie *dans le client* (chaque fichier V3X associé à un fichier de même taille), puis lance la vérification : elle ne démarre qu'à 100 %, sinon elle reste arrêtée. Rien n'est créé, déplacé ni renommé sur ton disque. Par prudence, D3X refuse quand un nom de la release V3X existe déjà, car le client déplacerait ce fichier : autre fichier de ta copie, autre torrent du client, ou fichier présent sur le disque. Le disque est vérifié par D3X quand il le voit (même machine, ou dossier partagé avec une correspondance de chemins), sinon demandé au client (qBittorrent 5, Deluge) ; quand personne ne peut le vérifier, D3X refuse au lieu de deviner. Possible avec qBittorrent, Deluge et Transmission (même rangement des dossiers, disque vu par D3X) ; rTorrent ne sait pas renommer les fichiers d'un torrent.

**Un Mac qui lit les fichiers d'un PC (partage réseau) ?** macOS décompose les lettres accentuées (la lettre, puis l'accent), même sur un partage Windows. Depuis la 1.1.42, D3X écrit les noms du .torrent et les chemins envoyés au client comme Windows et Linux : le client du PC retrouve les fichiers. Rien ne change sous Windows et Linux.

**Mon client tourne sur un NAS.** Publier lit les fichiers depuis ce PC (partage réseau) et ton client les seede là où il les voit : D3X traduit le chemin, en le devinant d'après les torrents que le client seede déjà dans ce dossier. Tu peux le corriger, il s'en souviendra (dans les dossiers de ce client).

**Une seedbox et des fichiers sur ce PC ?** Règle les deux clients : D3X les analyse ensemble, et chaque cross-seed part dans le client qui a les fichiers. Voir [Plusieurs clients](#plusieurs-clients). Les réglages restent lisibles par D3X 1.0 : il n'y retrouverait que le premier client.

## Compiler soi-même

Go 1.26 ou plus :

```sh
go test ./...
sh scripts/build-agents.sh   # agent de seedbox, facultatif
go build -trimpath -o d3x .
```

Sans `scripts/build-agents.sh`, tout marche sauf la seedbox.

Pour regarder l'interface sans compte ni client torrent, le mode démo lance la page avec un faux qBittorrent, un faux V3X et un faux MusicBrainz, peuplés de releases inventées, avec une médiathèque, des demandes et des relais (c'est lui qui a servi aux captures) :

```sh
D3X_DEMO=600 go test ./internal/server -run 'TestDemo$' -v
```

puis ouvrir `http://127.0.0.1:8799/auth?t=demo`. Avec `D3X_DEMO_CLIENTS=1`, la démo a trois clients : une seedbox, ce PC et un NAS éteint.

## Licence

[MIT](LICENSE). MediaInfo : [mediainfo.js](https://github.com/buzz/mediainfo.js) 0.3.7 (MediaInfoLib), licence BSD-2-Clause, voir [`internal/server/ui/vendor/mediainfo`](internal/server/ui/vendor/mediainfo).
