# Umsetzungsplan: Persönlicher Mix (personalisiertes Radio)

Status: Entwurf / Planung
Branch: `cursor/personalized-mix-2ac3`
Bezug: Evaluation „Option B (Fork/Core)"

## 1. Ziel

Ein „Persönlicher Mix": eine auf Knopfdruck erzeugte, gewichtet-zufällige Titelliste, die auf
den **Hörgewohnheiten des angemeldeten Nutzers** basiert (viel/kürzlich gespielt, geliked, hoch
bewertet), angereichert mit **ähnlichen Titeln** zur Entdeckung — vergleichbar mit Spotifys
personalisiertem Radio / „Daily Mix". Ergebnis wird direkt in die Player-Queue geladen.

Kein reiner Zufall (wie `getRandomSongs`) und kein reines Seed-Radio (wie `getSimilarSongs`),
sondern eine Mischung aus **Exploitation** (bekannte Favoriten) und **Exploration** (Neues/Ähnliches).

## 2. Abgrenzung zu bestehenden Funktionen

| Feature | Datei | Verhalten | Verhältnis zum Persönlichen Mix |
|---|---|---|---|
| `getRandomSongs` | `server/subsonic/album_lists.go`, `persistence/mediafile_repository.go` (`GetRandom`) | Gleichverteilter Zufall, ignoriert Annotationen | **Nicht ändern** (Subsonic-Standard); nur als Cold-Start-Fallback nutzen |
| `getSimilarSongs`/`2` | `server/subsonic/browsing.go`, `core/external/provider.go` (`SimilarSongs`) | Seed-basiert (1 Track/Album/Artist), extern (Last.fm/ListenBrainz) + `WeightedChooser`-Fallback | Baustein für Exploration; nicht habit-basiert |
| Jellyfin `InstantMix` | `server/jellyfin/similar.go` (`getInstantMix`) | Seed-basierter Mix (Finamp-Radio) | Precedent für „Mix"-Endpoint; wir bauen habit-basiert |
| Smart Playlists | `model/criteria/`, `persistence/criteria_sql.go` | Statische Regel-Playlist, `sort: random` | Kein gewichteter Zufall, kein Ähnlichkeits-Seed |
| SonicSimilarity | `core/sonic/`, Plugin | Akustische Ähnlichkeit (optionales Plugin) | Optionale zusätzliche Exploration-Quelle |

Fazit: Es gibt heute **keinen** Endpoint, der Nutzer-Hörgewohnheiten + Ähnlichkeit + gewichteten
Zufall kombiniert. Genau diese Lücke schließt das Feature.

## 3. Verfügbare Signale (bereits im Datenmodell)

Pro Nutzer in Tabelle `annotation` (`model/annotation.go`, `persistence/sql_annotations.go`,
per JOIN an `MediaFile`/`Album`/`Artist`):

- `play_count` (Häufigkeit), `play_date` (Aktualität)
- `starred`/`starred_at` (Loved), `rating`/`rated_at` (Bewertung)
- optional Einzel-Play-Historie in `scrobbles` (`model/scrobble.go`) bei `EnableScrobbleHistory`

Wiederverwendbare Bausteine:
- `utils/random/weighted_random_chooser.go` — `Add(value, weight)` / `Pick()` (ohne Zurücklegen).
- `external.Provider.SimilarSongs` / `TopSongs` (`core/external/provider.go`) — externe Ähnlichkeit,
  bereits auf die Bibliothek gematcht.
- `core/sonic/sonic.go` (`Sonic.GetSonicSimilarTracks`) — optional.
- `persistence/mediafile_repository.go` (`GetAll` mit Sort/Filter, `GetRandom`).

## 4. Architektur

Neuer, isolierter Core-Service + dünner Subsonic-Endpoint + kleine UI-Aktion. Bestehende Dateien
werden nur minimal berührt (Registrierung, DI, ein Response-Feld, ein UI-Menüeintrag), um den
Merge-Aufwand bei Upstream-Syncs gering zu halten.

### 4.1 Neuer Core-Service `core/mix`

Neue Dateien:
- `core/mix/mix.go`
- `core/mix/mix_test.go`

```go
package mix

type Options struct {
    Size          int      // Ziel-Anzahl (Default 50, Max 500)
    SeedID        string   // optional: Track/Album/Artist/Genre als expliziter Seed
    Genre         string   // optionaler Filter
    FromYear      int
    ToYear        int
    LibraryIDs    []int    // Bibliotheks-ACL des Nutzers
    DiscoveryRatio float64 // 0..1, Anteil Exploration (Default aus Config)
}

type Mixer interface {
    // PersonalMix erzeugt die Liste für den in ctx hinterlegten Nutzer.
    PersonalMix(ctx context.Context, opts Options) (model.MediaFiles, error)
}

func New(ds model.DataStore, provider external.Provider, sonic *sonic.Sonic) Mixer
```

Der Service liest den angemeldeten Nutzer aus dem Context (wie andere Core-Services), sodass
Annotationen automatisch nutzerspezifisch gejoint werden (`persistence/sql_annotations.go`
`withAnnotation` nutzt `loggedUser(ctx)`).

### 4.2 Neuer Subsonic-Endpoint

Neue Dateien:
- `server/subsonic/mix.go` (Handler `GetPersonalMix`)
- `server/subsonic/mix_test.go`

Änderungen an bestehenden Dateien:
- `server/subsonic/api.go`:
  - `Router`-Struct + `New(...)` um `mixer mix.Mixer` erweitern.
  - Registrierung: `h(r, "getPersonalMix", api.GetPersonalMix)` in der Discovery-Gruppe
    (bei `getRandomSongs`, ca. Zeile 139).
- `server/subsonic/responses/responses.go`:
  - Neues Feld `PersonalMix *Songs` in `Subsonic` (analog `RandomSongs *Songs`, Zeile 33).

Handler-Muster (analog `GetSimilarSongs`, `server/subsonic/browsing.go`):

```go
func (api *Router) GetPersonalMix(r *http.Request) (*responses.Subsonic, error) {
    ctx := r.Context()
    p := req.Params(r)
    size := min(p.IntOr("size", 50), 500)
    libs, _ := selectedMusicFolderIds(r, false)
    songs, err := api.mixer.PersonalMix(ctx, mix.Options{
        Size: size, SeedID: p.StringOr("seedId", ""),
        Genre: p.StringOr("genre", ""),
        FromYear: p.IntOr("fromYear", 0), ToYear: p.IntOr("toYear", 0),
        LibraryIDs: libs,
    })
    if err != nil { return nil, err }
    resp := newResponse()
    resp.PersonalMix = &responses.Songs{Song: slice.MapWithArg(songs, ctx, childFromMediaFile)}
    return resp, nil
}
```

Auth/ACL: läuft in der `authenticate`-Gruppe; Bibliotheksfilter über `selectedMusicFolderIds` +
`u.HasLibraryAccess` (wie in Jellyfin `similarSongs`).

### 4.3 DI / Wire

- `core/wire_providers.go`: `mix.New` hinzufügen (bei `external.NewProvider`, Zeile ~31).
- `cmd/wire_injectors.go`: `mix.New` ins Provider-Set; `subsonic.New` erhält neuen Parameter.
- `make wire` (`go tool wire`) neu generieren → `cmd/wire_gen.go`.
- Alle `subsonic.New(...)`-Aufrufstellen an die neue Signatur anpassen:
  - `server/subsonic/opensubsonic_test.go` (2 Stellen)
  - `server/subsonic/e2e/e2e_suite_test.go`, `server/subsonic/e2e/subsonic_sonic_similarity_test.go`

### 4.4 Konfiguration

`conf/configuration.go`: verschachtelte Options-Struct analog `jellyfinOptions` (Zeile 120):

```go
type personalMixOptions struct {
    Enabled        bool
    DiscoveryRatio float64 // Default 0.4
    MaxPerArtist   int     // Default 2 (Diversität)
    RecencyHalfLife time.Duration // Default 30d (Recency-Decay)
    SeedSize       int     // Anzahl Seed-Tracks/Artists (Default 25)
}
```
Feature standardmäßig aktiv; Gewichte als Dev-tunable Defaults.

### 4.5 UI (react-admin)

- `ui/src/subsonic/index.js`: `getPersonalMix(size)` (analog `getSimilarSongs2`).
- `ui/src/common/playbackActions.js`: `playPersonalMix(dispatch, notify)` (analog `playSimilar`),
  ruft Endpoint, dispatcht `playTracks(songData, ids)`.
- Einstiegspunkt(e): Menüeintrag/Button, z.B. in `ui/src/common/SongContextMenu.jsx`,
  Album/Artist-Actions oder als globaler „Mix starten"-Button (Album-Liste/Startseite).
- i18n: neue Keys in `ui/src/i18n/en.json` (+ ggf. `de.json`), z.B. `personalMix`, Meldungen für
  „kein Mix möglich".

### 4.6 Optionale Erweiterungen (später)

- Jellyfin: „Suggestions"/Seed-loser Mix in `server/jellyfin/` (Precedent `getInstantMix`).
- Native REST-API (`server/nativeapi/`) für den eigenen React-Client, falls Subsonic-Weg unerwünscht.

## 5. Algorithmus-Design

Eingaben: Nutzer (aus ctx), `Options`. Ablauf:

1. **Signal-Ermittlung** (nutzerspezifisch, via DataStore):
   - Top-N nach `play_count` (Frequency)
   - Top-N nach `play_date` desc (Recency)
   - `starred = true` (Loved)
   - `rating >= 4` (Bewertung)
   - Ableitung der Lieblings-Artists/Genres aus obigen Mengen.
2. **Seeds** bestimmen (bei explizitem `SeedID` diesen bevorzugen; sonst aus Signalen).
3. **Kandidaten** sammeln:
   - Exploitation: die Signal-Titel selbst.
   - Exploration: `provider.SimilarSongs`/`TopSongs` für Seed-Tracks/Artists; optional
     `sonic.GetSonicSimilarTracks`. Ergebnisse sind bereits auf die Bibliothek gematcht.
   - Discovery: wenig/nie gespielte Titel derselben Genres/Artists (`GetAll` mit Filter).
4. **Scoring** je Kandidat:
   `weight = w_freq·norm(playCount) + w_rec·decay(playDate) + w_loved·starred + w_rating·norm(rating)
   + w_sim·similarity − penalty(kürzlich gespielt / bereits in Queue) + noise`
   mit exponentiellem Recency-Decay (Halbwertszeit aus Config).
5. **Auswahl** mit `WeightedChooser.Pick()` ohne Zurücklegen bis `Size`, unter
   **Diversitäts-Constraints** (`MaxPerArtist`, Artist/Album-Cooldown), damit kein Künstler dominiert.
6. **Exploit/Explore-Verhältnis** über `DiscoveryRatio` steuern.
7. **Fallbacks**:
   - Kalter Start (keine Historie) → `GetRandom` (Verhalten wie heute) bzw. global beliebte Titel.
   - Keine konfigurierten Agents/keine externe Ähnlichkeit → nur lokale Signale + Genre-Discovery.
   - Provider langsam/fehlerhaft → Timeout, Exploration-Anteil degradiert zu lokalem Zufall
     (Muster: Timeout/`singleflight` wie in `server/jellyfin/similar.go`).

## 6. API-Design

- Endpoint: `getPersonalMix` (Custom-Erweiterung; OpenSubsonic erlaubt zusätzliche Endpoints).
  Ggf. als OpenSubsonic-Extension deklarieren (`getOpenSubsonicExtensions`).
- Parameter: `size` (Default 50, Max 500), optional `seedId`, `genre`, `fromYear`, `toYear`,
  `musicFolderId` (mehrfach), optional `discovery` (0..1).
- Response: neues Feld `personalMix` vom Typ `Songs` (JSON + XML), Kinder via `childFromMediaFile`.
- Fehlerverhalten: leere Liste statt Fehler, wenn nichts erzeugbar (Client-freundlich, wie Jellyfin).

## 7. Teststrategie

- **Unit** (`core/mix/mix_test.go`, ginkgo wie übrige Core-Tests): gemockter `DataStore`/`Provider`.
  Determinismus-Hinweis: `utils/random` nutzt `crypto/rand` (nicht seedbar) → entweder in `core/mix`
  eine **injizierbare RNG-Quelle** einführen (empfohlen, für deterministische Tests) oder auf
  **Invarianten** testen (Größe, Mitgliedschaft, `MaxPerArtist`, Fallback bei leerer Historie,
  Exploit/Explore-Anteil, ACL-Filter).
- **Subsonic-Handler** (`server/subsonic/mix_test.go`): Param-Parsing, Response-Struktur, Fehlerfälle.
- **e2e** (`server/subsonic/e2e/`): analog `subsonic_sonic_similarity_test.go`.
- **UI**: `playbackActions`/`SongContextMenu.test.jsx`-Muster (mock `subsonic.getPersonalMix`).
- **Manuell**: `make dev`, „Mix starten"-Button, Wiedergabe verifizieren (computerUse) + `curl`
  gegen `getPersonalMix`; Cold-Start (frischer Nutzer) und aktiver Nutzer prüfen.
- Vor Push: `make lintall` + `make testall` (entspricht pre-push-Hook).

## 8. Fork-/Upstream-Pflege

- Neuer Code liegt überwiegend in **neuen Dateien** (`core/mix/`, `server/subsonic/mix.go`,
  `ui/src/...`). Eingriffe in bestehende Dateien sind klein und lokal (Registrierung, DI,
  Response-Feld, UI-Menü, Config) → geringes Konfliktrisiko bei `chore: sync upstream`.
- Feature hinter Config-Flag `PersonalMix.Enabled`.
- `make wire` nach Signaturänderung nicht vergessen (generierter Code).

## 9. Phasenplan (inkrementell, jederzeit lauffähig)

- **Phase 0 – Gerüst & E2E-Durchstich**
  - Branch auf aktuellem `master` inkl. Build-Fix (erledigt).
  - `core/mix` mit Interface + Cold-Start-Fallback (nur `GetRandom`), Endpoint + Registrierung +
    Response-Feld, Wire, minimaler UI-Button. → Feature end-to-end sichtbar (noch „nur Zufall").
- **Phase 1 – Habit-basiert (lokal)**
  - Signale (freq/recency/loved/rating) + `WeightedChooser` + Diversitäts-Constraints, ohne externe
    Ähnlichkeit. Config-Gewichte. Unit-Tests.
- **Phase 2 – Exploration**
  - `provider.SimilarSongs`/`TopSongs` (+ optional `sonic`), Exploit/Explore-Mix, Anti-Repetition,
    Provider-Timeouts. e2e-Tests.
- **Phase 3 – Feinschliff**
  - Performance/Caching, Config-Tuning, optional Jellyfin-/native-Anbindung, Doku, Übersetzungen,
    Tests vervollständigen.

## 10. Risiken & offene Fragen

- **Performance** bei großen Bibliotheken (Annotation-Queries + externe Ähnlichkeit): Limits,
  Caching, Timeouts.
- **Test-Determinismus** wegen `crypto/rand` → injizierbare RNG in `core/mix` einplanen.
- **Endpoint-Name/OpenSubsonic-Kompatibilität** final festlegen.
- **Kalter Start** / Nutzer ohne Historie → sinnvoller Fallback.
- **Externe Ähnlichkeit** erfordert konfigurierte Agents (Last.fm/ListenBrainz); ohne diese nur
  lokale Signale.
- **Mehrbenutzer/Bibliotheks-ACLs** konsequent anwenden.

## 11. Betroffene Dateien (Übersicht)

Neu:
- `core/mix/mix.go`, `core/mix/mix_test.go`
- `server/subsonic/mix.go`, `server/subsonic/mix_test.go`
- `ui/src/…` (Aktion + ggf. Button-Komponente)

Geändert:
- `server/subsonic/api.go` (Router-Feld, `New`-Signatur, Registrierung)
- `server/subsonic/responses/responses.go` (Feld `PersonalMix`)
- `core/wire_providers.go`, `cmd/wire_injectors.go`, `cmd/wire_gen.go` (generiert)
- `conf/configuration.go` (`personalMixOptions`)
- `ui/src/subsonic/index.js`, `ui/src/common/playbackActions.js`,
  `ui/src/common/SongContextMenu.jsx` (o.ä.), `ui/src/i18n/en.json`
- Test-Aufrufstellen von `subsonic.New` (`server/subsonic/opensubsonic_test.go`,
  `server/subsonic/e2e/*`)
