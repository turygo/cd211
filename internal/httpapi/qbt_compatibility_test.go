package httpapi

import (
	"strings"
	"testing"
)

type qbtRouteContract struct{ path, required, shape string }

// qB 5.0 / WebAPI 2.11 route packet. Keep this inventory independent of
// implementation so adding or removing a handler cannot silently narrow the
// advertised compatibility surface.
var qbtRouteManifest = func() []qbtRouteContract {
	const packet = `app/buildInfo||object
app/defaultSavePath||string
app/getDirectoryContent|dirPath|array
app/networkInterfaceAddressList|iface|array
app/networkInterfaceList||array
app/preferences||object
app/sendTestEmail||empty
app/setPreferences|json|empty
app/shutdown||empty
app/version||string
app/webapiVersion||string
auth/login||text
auth/logout||empty
log/main||array
log/peers||array
rss/addFeed|url,path|empty
rss/addFolder|path|empty
rss/items||object
rss/markAsRead|itemPath|empty
rss/matchingArticles|ruleName|object
rss/moveItem|itemPath,destPath|empty
rss/refreshItem|itemPath|empty
rss/removeItem|path|empty
rss/removeRule|ruleName|empty
rss/renameRule|ruleName,newRuleName|empty
rss/rules||object
rss/setFeedURL|path,url|empty
rss/setRule|ruleName,ruleDef|empty
search/delete|id|empty
search/downloadTorrent|torrentUrl,pluginName|empty
search/enablePlugin|names,enable|empty
search/installPlugin|sources|empty
search/plugins||array
search/results|id|object
search/start|pattern,category,plugins|object
search/status|id|array
search/stop|id|empty
search/uninstallPlugin|names|empty
search/updatePlugins||empty
sync/maindata||object
sync/torrentPeers||object
torrents/SSLParameters|hash|object
torrents/add||text
torrents/addPeers|hashes,peers|object
torrents/addTags|hashes,tags|empty
torrents/addTrackers|hash,urls|empty
torrents/bottomPrio|hashes|empty
torrents/categories||object
torrents/count||string
torrents/createCategory|category|empty
torrents/createTags|tags|empty
torrents/decreasePrio|hashes|empty
torrents/delete|hashes,deleteFiles|empty
torrents/deleteTags|tags|empty
torrents/downloadLimit|hashes|object
torrents/editCategory|category,savePath|empty
torrents/editTracker|hash,origUrl,newUrl|empty
torrents/export|hash|bytes
torrents/filePrio|hash,id,priority|empty
torrents/files|hash|array
torrents/increasePrio|hashes|empty
torrents/info||array
torrents/pieceHashes|hash|array
torrents/pieceStates|hash|array
torrents/properties|hash|object
torrents/reannounce|hashes|empty
torrents/recheck|hashes|empty
torrents/removeCategories|categories|empty
torrents/removeTags|hashes|empty
torrents/removeTrackers|hash,urls|empty
torrents/rename|hash,name|empty
torrents/renameFile|hash,oldPath,newPath|empty
torrents/renameFolder|hash,oldPath,newPath|empty
torrents/setAutoManagement|hashes,enable|empty
torrents/setCategory|hashes,category|empty
torrents/setDownloadLimit|hashes,limit|empty
torrents/setDownloadPath|id,path|empty
torrents/setForceStart|hashes,value|empty
torrents/setLocation|hashes,location|empty
torrents/setSSLParameters|hash|empty
torrents/setSavePath|id,path|empty
torrents/setShareLimits|hashes,ratioLimit,seedingTimeLimit,inactiveSeedingTimeLimit|empty
torrents/setSuperSeeding|hashes,value|empty
torrents/setUploadLimit|hashes,limit|empty
torrents/start|hashes|empty
torrents/stop|hashes|empty
torrents/tags||array
torrents/toggleFirstLastPiecePrio|hashes|empty
torrents/toggleSequentialDownload|hashes|empty
torrents/topPrio|hashes|empty
torrents/trackers|hash|array
torrents/uploadLimit|hashes|object
torrents/webseeds|hash|array
transfer/banPeers|peers|empty
transfer/downloadLimit||string
transfer/info||object
transfer/setDownloadLimit|limit|empty
transfer/setSpeedLimitsMode|mode|empty
transfer/setUploadLimit|limit|empty
transfer/speedLimitsMode||string
transfer/toggleSpeedLimitsMode||empty
transfer/uploadLimit||string`
	lines := strings.Split(strings.TrimSpace(packet), "\n")
	result := make([]qbtRouteContract, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, "|")
		result = append(result, qbtRouteContract{parts[0], parts[1], parts[2]})
	}
	return result
}()

func TestQBTRouteManifestIsComplete(t *testing.T) {
	if len(qbtRouteManifest) != 102 {
		t.Fatalf("qB route manifest has %d routes, want 102", len(qbtRouteManifest))
	}
	seen := make(map[string]struct{}, len(qbtRouteManifest))
	for _, route := range qbtRouteManifest {
		if route.path == "" || route.shape == "" {
			t.Fatalf("invalid manifest entry: %+v", route)
		}
		if _, exists := seen[route.path]; exists {
			t.Fatalf("duplicate route %q", route.path)
		}
		seen[route.path] = struct{}{}
	}
}
