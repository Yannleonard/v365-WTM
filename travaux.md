# Travaux UniHV

Ce fichier coordonne les tâches restantes pour rendre l'application UniHV 100 % fonctionnelle en production.
Il est conçu pour être utilisé par les agents experts et par l’équipe QA.

## 0. Journal de coordination (coordination-lead)

- **2026-06-08 (soir)** — Hosts réels **Xen / ESXi / Hyper-V disponibles DEMAIN MATIN**.
  Stratégie validée avec l'owner :
  - **Ce soir** : écrire le VRAI code d'export live (Hyper-V VHDX via WMI, ESXi OVF/VMDK via
    govmomi HttpNfcLease, Xen XVA via XAPI) + SMTP réel. Validation contre les simulateurs
    officiels (vcsim, wire XAPI, WMI fake) + build/tests verts. **Aucun faux succès** : tant
    qu'un host réel n'a pas confirmé, le live renvoie une erreur claire si l'export ne peut
    pas réellement streamer — jamais de placeholder.
  - **Demain matin** : brancher les 3 hosts réels (IP + identifiants fournis par l'owner),
    créer les connexions hyperviseur dans l'app, et **prouver chaque export en réel**.
  - Réel confirmé aujourd'hui : **KVM/libvirt uniquement** (host WSL local).

## 1. État actuel constaté

- L’application fonctionne déjà dans Docker sur `http://localhost:8080/`.
- Le backend Go et l’UI sont présents, avec une base Castor importée et des fonctions hyperviseur actives.
- Les documents de progression indiquent un socle très avancé, mais le code source montre encore des bouches d’aération critiques :
  - `server/internal/vprovider/hyperv/provider.go` : export Hyper-V live encore `ErrUnsupported`.
  - `server/internal/vprovider/esxi/esxi.go` : export ESXi live encore `ErrUnsupported`.
  - `server/internal/vprovider/xen/xen.go` : export Xen live encore `ErrUnsupported`.
  - `server/internal/alarms/alarms.go` : canal `email-stub` uniquement journalisé, pas de SMTP réel.
  - `server/internal/vprovider/kvm/kvm.go` : export KVM non-file backed disks renvoie une erreur claire, ce qui est acceptable, mais doit être validé comme comportement final.

## 2. Objectif global

Faire en sorte que l’application soit :
1. déployable et fonctionnelle dans Docker Desktop,
2. vérifiée sur une connexion Hyper-V / KVM réelle,
3. exempte de faux succès en production,
4. dotée de notifications email réelles,
5. couverte par des tests de build, API et validation live.

## 3. Priorités immédiates

1. Vérifier et valider l’état réel des exports de VM pour Hyper-V, ESXi et Xen.
2. Implémenter les exports live manquants (VHDX / OVF/VMDK / XVA).
3. Remplacer `email-stub` par un vrai canal SMTP configurable tout en conservant un mode stub pour CI hors ligne.
4. Valider le comportement KVM export pour disques non-file, en documentant le cas d’usage et l’erreur.
5. Exécuter la chaîne de vérification finale : Go build + tests Docker, UI build + tests, et validation live via `virsh` / app web.

## 4. Agents et responsabilités

### 4.1 Agent `hyperv-expert`

Objectif : supprimer la dernière implémentation stub pour Hyper-V et livrer un export réel.

Tâches :
- Fichier principal : `server/internal/vprovider/hyperv/provider.go`
- Implémenter `ExportVM()` pour la route live Hyper-V `Export-VM` / VHDX streaming via WMI.
- Ajouter ou compléter les tests dans `server/internal/vprovider/hyperv/hyperv_conformance_test.go`.
- Vérifier que le comportement live retourne bien un flux de données réelles, pas un placeholder.
- Mettre à jour le contrat `CapabilityMatrix` si nécessaire.

### 4.2 Agent `esxi-expert`

Objectif : rendre l’export ESXi réel et non plus un placeholder.

Tâches :
- Fichier principal : `server/internal/vprovider/esxi/esxi.go`
- Implémenter `ExportVM()` avec `govmomi` HttpNfcLease / VMDK export.
- Ajouter une gestion d’erreur claire si le host ne supporte pas l’export.
- Ajouter des tests unitaires / de conformance en `server/internal/vprovider/esxi`.

### 4.3 Agent `xen-expert`

Objectif : rendre l’export Xen réel.

Tâches :
- Fichier principal : `server/internal/vprovider/xen/xen.go`
- Implémenter l’export live XAPI/XVA via un HTTP handler ou streaming direct.
- S’assurer que la route renvoie une erreur claire en cas de refus ou d’absence de fonctionnalité.
- Ajouter tests correspondants.

### 4.4 Agent `kvm-expert`

Objectif : valider le comportement d’export KVM et fermer les cas limites.

Tâches :
- Fichier principal : `server/internal/vprovider/kvm/kvm.go`
- Revoir `ExportVM()` et `exportDisk()` pour confirmer que :
  - les disques file-backed qcow2 sont exportés réellement via `qemu-img`.
  - les disques non-file (block/RBD/iSCSI) échouent proprement avec `ErrUnsupported`.
- Documenter précisément ce comportement et ajouter un test live si possible.
- Si possible, identifier si un chemin d’export réseau peut être ajouté pour ces sources.

### 4.5 Agent `alarms-expert`

Objectif : activer un canal email réel et retirer le seul mode stub en production.

Tâches :
- Fichier principal : `server/internal/alarms/alarms.go`
- Ajouter un vrai émetteur SMTP configurable (`SMTP server`, port, login, from, TLS).
- Conserver `email-stub` comme option de test/CI, mais ne plus l’exposer comme seule option en prod.
- Mettre à jour l’UI et l’API si nécessaire pour gérer les paramètres SMTP.
- Ajouter tests d’envoi SMTP et tests de validation de config.

### 4.6 Agent `frontend-engineer`

Objectif : vérifier que l’UI expose correctement les nouvelles capacités et ne laisse pas l’utilisateur croire à un succès factice.

Tâches :
- Vérifier les vues `Alarms.tsx` et `Backups.tsx` / `VMDetail`.
- S’assurer que les boutons/export ne sont accessibles que si les capacités live sont présentes.
- Mettre à jour les messages d’erreur/état afin que l’utilisateur voie clairement les actions non supportées.
- Exécuter `cd ui && npm run build && npm test` et corriger les regressions.

### 4.7 Agent `qa-engineer`

Objectif : valider la version la plus récente jusqu’à 100 % fonctionnel.

Tâches :
- Exécuter la suite Go dans Docker comme indiqué dans `COORDINATION.md`.
- Exécuter `cd ui && npm run build && npm test`.
- Vérifier la plateforme en live sur `http://localhost:8080/`.
- Valider au moins : inventaire, power operations, console, backup/export, alarm SMTP.
- Produire un rapport de validation avec résultats et captures d’écran/commandes.

### 4.8 Agent `coordination-lead`

Objectif : maintenir ce fichier à jour et vérifier l’état de chaque tâche.

Tâches :
- Actualiser `travaux.md` en fin de journée.
- Faire le relais entre les agents `backend`, `frontend`, `qa`, `security`.
- Surveiller les points bloquants et adresser toute divergence entre documentation et réalité.

### 4.9 Assignations initiales

Chaque agent doit maintenir son statut dans `travaux.md` et reporter ici l’avancement.

- `hyperv-expert` : implémentation et tests de `ExportVM()` live Hyper-V. Objectif = vrai flux VHDX.
- `esxi-expert` : implémentation et tests de `ExportVM()` live ESXi. Objectif = vrai OVF/VMDK HttpNfcLease.
- `xen-expert` : implémentation et tests de `ExportVM()` live Xen. Objectif = vrai XVA/XAPI streaming.
- `kvm-expert` : validation et renforcement de l’export KVM live. Objectif = exporter les disques file-backed et documenter le comportement `ErrUnsupported` pour les autres.
- `alarms-expert` : implémenter un canal SMTP réel dans `server/internal/alarms/alarms.go` tout en conservant un mode stub pour CI.
- `frontend-engineer` : vérifier et corriger l’UI pour n’afficher que les actions réellement supportées, puis relancer `npm run build && npm test`.
- `qa-engineer` : valider la recette complète en local sur `http://localhost:8080/` et sur les flux live de l’hyperviseur.
- `coordination-lead` : suivre chaque tâche, mettre à jour le fichier et ouvrir un ticket de blocage si nécessaire.

## 5. Critères d’acceptation

1. `docker build -t unihv:latest --build-arg VERSION=<tag> .` passe.
2. `cd ui && npm run build && npm test` passe.
3. `CGO_ENABLED=0 go test ./server/...` passe dans Docker.
4. `http://localhost:8080/` est accessible et l’inventaire KVM fonctionne.
5. Les exports Hyper-V/ESXi/Xen ne renvoient plus `ErrUnsupported` en mode live.
6. Le canal email est opérationnel via SMTP réel.
7. Aucune fausse réussite ne doit exister dans le chemin de production.

## 6. Validation continue

- Utiliser les commandes de build/test du `COORDINATION.md`.
- Exécuter des tests live sur WSL/libvirt :
  - `wsl -d Ubuntu -u root -- bash -lc "virsh -c qemu:///system list --all"`
- Vérifier la session Docker Desktop et l’accès `host.docker.internal:16509`.
- Maintenir un registre des bugs réels trouvés et de leur résolution.

## 7. Gouvernance des agents

Les agents sont ordonnés de fonctionner de manière autonome et de se synchroniser toutes les 10 minutes.
Ils doivent :

1. Prendre leurs ordres dans `travaux.md` et dans ce document.
2. Valider chaque modification par des builds/tests et des preuves réelles avant de passer à la suivante.
3. Reporter immédiatement toute demande de validation ou doute dans le même fichier, sans solliciter l’utilisateur pour arbitrage.
4. Ne jamais ajouter de mock sur le chemin de production ; si une action ne peut pas être réalisée, elle doit renvoyer une erreur claire.
5. Respecter la règle : l’utilisateur n’est pas consulté pour des décisions techniques, sauf si un blocage externe le nécessite.

### 7.1 Règles de cadence

- Chaque agent doit se mettre à jour dans `travaux.md` toutes les 10 minutes au moins.
- Si un agent termine une tâche ou rencontre un blocage, il inscrit le statut, les résultats et l’étape suivante.
- Le `coordination-lead` valide les changements et ferme les tickets internes dans le document.

### 7.2 Autorité du coordination-lead

Le `coordination-lead` a la responsabilité de :

- vérifier et valider les livrables techniques.
- arbitrer les priorités entre agents.
- refuser toute implémentation qui ne respecte pas la règle "pas de faux succès".

Ce fichier fait office de commandement : les agents doivent suivre scrupuleusement les sections 3, 4 et 7.

---

> Note : ce fichier est le plan de travail principal. Les agents doivent l’utiliser comme source unique de vérité et le mettre à jour après chaque tâche majeure.
