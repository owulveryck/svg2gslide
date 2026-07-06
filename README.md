# svg2gslide

Convertit un fichier SVG en une slide **native** Google Slides (formes, lignes
et zones de texte éditables — pas une image), ajoutée à la fin d'une
présentation existante.

## Usage

```sh
go run . -svg testdata/sdlc-phase-8.svg \
  -presentation <PRESENTATION_ID> \
  -out-thumbnail /tmp/slide.png \
  -v
```

Flags :

| Flag | Description |
|---|---|
| `-svg` | fichier SVG d'entrée (requis) |
| `-presentation` | ID de la présentation cible (requis) |
| `-credentials` | JSON client OAuth ou service account (défaut : `$SLIDES_CREDENTIALS`, puis `~/.config/gcloud/slideappscripter-client.json`) |
| `-phase` | force la phase active (défaut : attribut `data-active-phase` du SVG) |
| `-out-thumbnail` | télécharge le thumbnail PNG de la nouvelle slide |
| `-export-pdf` | exporte la présentation complète en PDF |
| `-v` | affiche les éléments ignorés/approximés |

Utilitaire d'entretien :

```sh
go run ./cmd/presctl -presentation <ID> -delete-slide <SLIDE_OBJECT_ID> -export-pdf /tmp/deck.pdf
```

## Fonctionnement

1. Parsing du SVG (`internal/svg`) et évaluation **statique** du CSS de
   visibilité par phase (`[data-active-phase="N"]`) — les animations
   (`@keyframes`, dots, highlights) sont ignorées.
2. Mapping vers des requêtes `batchUpdate` (`internal/mapper`) :
   - `rect` → RECTANGLE / ROUND_RECTANGLE, `circle` → ELLIPSE ;
   - `line` → connecteur STRAIGHT (flèche si `marker-end`) ;
   - quarts de cercle (`A` alignés sur les axes) → forme **ARC** native,
     orientée par quadrant via les flips scaleX/scaleY ;
   - courbes quadratiques (`Q`) → connecteur CURVED, scindé au point milieu
     si la courbe est profonde ;
   - polygones à 3 points (chevrons) → TRIANGLE avec rotation ;
   - groupes « boîte 3D » (≥3 polygones) → forme CUBE ;
   - `text` → TEXT_BOX centrée (police, taille, gras/italique, couleur).
3. Une seule slide BLANK est créée puis remplie en un `batchUpdate` (chunké
   au-delà de 400 requêtes).

## Approximations connues

- Chemins fermés à courbes cubiques (nuages…) : ignorés (avec warning `-v`).
- Halo « surligneur » des tspans : rendu en gras simple.
- La police du SVG (ex. `Outfit`) doit exister côté Google Fonts.

## Validation visuelle

`rsvg-convert` rend ces SVG **blancs** (librsvg n'applique pas le CSS de
phase). Pour une référence fidèle, utiliser Chrome headless :

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless \
  --screenshot=/tmp/ref.png --window-size=2000,1680 \
  "file://$PWD/testdata/sdlc-phase-8.svg"
```

puis comparer avec le thumbnail produit par `-out-thumbnail`.
