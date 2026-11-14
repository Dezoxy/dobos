// People and the systems around Flodni. Everything here except Flodni itself is
// outside this repository.

operator = person "Operator" "Runs Flodni, adds and removes titles, and watches its health."
viewer = person "Viewer" "Watches films and series through the media server."

mediaServer = softwareSystem "Media Server" "Plex. Scans the library and plays files for viewers." "External"
automation = softwareSystem "Library Automation" "Scripts and services that decide which releases to add or remove." "External"
swarm = softwareSystem "BitTorrent Swarm" "Peers and trackers that hold the pieces of each torrent." "External"

viewer -> mediaServer "Watches titles on" "HTTPS"
