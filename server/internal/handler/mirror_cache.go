package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"golang.org/x/sync/singleflight"
)

// ---------------------------------------------------------------------------
// TEA-115 verified-mirror cache (INV-18 / INV-20 / single-flight).
//
// This is a pure server-side, lazy cache-through with single-flight origin
// fetch. It backs the four mirror download handlers (fleet_mirror.go). The
// server is a DUMB cache that makes ZERO security adjudication (INV-19): it
// stores and serves GitHub-origin bytes; the daemon re-verifies every byte
// against the embedded trusted root + SAN triad (INV-16). Nothing here parses,
// validates, or trusts the artifact contents.
//
// Three buckets, never conflated (INV-20):
//   ① immutable  — release archive + checksums.txt + binary-artifact attestation
//                  bundles. Content-addressed (key = digest or tag/asset).
//                  Never expires; LRU capacity eviction only.
//   ② pointer    — /releases/latest metadata. 60s TTL + serve-stale-on-upstream-fail.
//   ③ revocation — revocations.json CONTENT bytes + its OWN attestation bundle,
//                  bound as ONE ATOMIC cache unit (same TTL, fetched together,
//                  serve-staled together — BLOCKER-2). Short TTL (≤60s) +
//                  serve-stale. NEVER folded into the immutable bucket: the
//                  revocation table is re-signed in place without a new release,
//                  so caching it as immutable would let the mirror amplify the
//                  miss-revocation window from v4's accepted ~60s to days.
//
// Negative-result caching (INV-18 / review suggestion ②): an upstream 404 is an
// authoritative "this artifact/digest objectively does not exist" — cacheable
// with a SHORT TTL (mirrorNegativeTTL ≤30s) to blunt high-frequency probing of
// random digests against the public endpoint. A 504/timeout/connection error is
// transient and MUST NOT be cached (it would poison the cache into reporting
// "no attestation" / serve a stale value forever); it serve-stales a verified
// cached value or surfaces the error so the next request re-origins. This is a
// SERVER cache-correctness property, NOT a daemon behaviour dependency — the
// daemon treats any non-200 uniformly (INV-16).
// ---------------------------------------------------------------------------

const (
	// mirrorPointerTTL is bucket ② TTL for /releases/latest.
	mirrorPointerTTL = 60 * time.Second
	// mirrorReleaseTagTTL is the resident-cache TTL for per-tag release metadata
	// (the binary-anchor hot path). Release metadata is effectively immutable once
	// a tag is published, so 60s is ample; it reuses the bucket-② TTL+serve-stale
	// shape so N daemons resolving the same tag's metadata cost GitHub ONE upstream
	// metadata call per version, not N. Closes the review's getReleaseByTag hot
	// path: previously there was no resident positive cache, so every daemon
	// re-origined the tag metadata (callsReleaseByTag == N).
	mirrorReleaseTagTTL = 60 * time.Second
	// mirrorRevocationTTL is bucket ③ TTL for the revocations atomic unit. ≤60s
	// per INV-20 so the mirror never amplifies the miss-revocation window.
	mirrorRevocationTTL = 60 * time.Second
	// mirrorNegativeTTL is the short TTL for a cached upstream-404 negative
	// result (review suggestion ②): long enough to absorb a retry storm, short
	// enough that a freshly-published artifact becomes visible quickly and that
	// random-digest probing cannot pin a large negative cache.
	mirrorNegativeTTL = 30 * time.Second
	// mirrorImmutableMaxEntries caps the immutable bucket; LRU evicts the
	// least-recently-used entry past this. Archives/checksums/attestations are
	// content-addressed and re-fetchable, so eviction is safe (re-origins once).
	mirrorImmutableMaxEntries = 4096
	// mirrorReleaseTagMaxEntries caps the per-tag release-metadata resident cache
	// (releaseByTag). Without a bound, the mirror routes being PUBLIC and the tag
	// key coming straight off the URL path lets an attacker pin unbounded entries
	// by probing /releases/tags/<random-unique> — INCLUDING the short-TTL 404
	// negatives, which are themselves attacker-controlled random tags — until the
	// process OOMs. Rate limiting only throttles the rate, not long-term unbounded
	// growth. Both POSITIVE and NEGATIVE entries share this single LRU bound. A
	// normal fleet sees a handful of live tags, so 1024 is ample headroom; evicted
	// entries are re-fetchable (TTL'd, re-origins once), so eviction is safe.
	mirrorReleaseTagMaxEntries = 1024
)

// mirrorOriginFetcher is the upstream-fetch seam. Production wires the
// cli.*FromGitHub helpers; tests inject fakes that count upstream calls and
// inject 404/504 without touching the network.
type mirrorOriginFetcher interface {
	// fetchReleaseByTag returns release metadata for tag, or a cli.MirrorNotFound
	// on upstream 404.
	fetchReleaseByTag(tag string) (*cli.GitHubRelease, error)
	// fetchLatestRelease returns the latest release metadata, or cli.MirrorNotFound.
	fetchLatestRelease() (*cli.GitHubRelease, error)
	// fetchAttestation returns the raw GitHub attestations API bytes (sigstore
	// bundle JSON, byte-for-byte) for digestHex, or cli.MirrorNotFound on 404.
	fetchAttestation(digestHex string) ([]byte, error)
	// fetchAsset downloads the bytes at a GitHub-authoritative download URL.
	fetchAsset(downloadURL string) ([]byte, error)
}

// realMirrorOrigin wires the cli GitHub-pinned origin helpers.
type realMirrorOrigin struct {
	timeout time.Duration
}

func (o realMirrorOrigin) fetchReleaseByTag(tag string) (*cli.GitHubRelease, error) {
	return cli.FetchReleaseByTag(tag)
}
func (o realMirrorOrigin) fetchLatestRelease() (*cli.GitHubRelease, error) {
	return cli.FetchLatestReleaseFromGitHub()
}
func (o realMirrorOrigin) fetchAttestation(digestHex string) ([]byte, error) {
	return cli.FetchAttestationBundleBytesFromGitHub(digestHex, o.timeout)
}
func (o realMirrorOrigin) fetchAsset(downloadURL string) ([]byte, error) {
	return cli.FetchAssetBytesFromGitHub(downloadURL, o.timeout)
}

// immutableEntry is a content-addressed blob in bucket ①.
type immutableEntry struct {
	bytes      []byte
	negative   bool      // upstream 404 negative result (no bytes)
	negExpires time.Time // negative result expiry (zero for positive entries)
}

// ttlEntry is a TTL'd value (bucket ② / negative caches): bytes + a typed
// release pointer (for /latest) + expiry; serve-stale keeps the last good value.
type ttlEntry struct {
	release  *cli.GitHubRelease // for /latest
	bytes    []byte             // for byte payloads
	negative bool
	expires  time.Time
}

func (e ttlEntry) fresh() bool { return time.Now().Before(e.expires) }

// revocationUnit is the BLOCKER-2 atomic cache unit: the revocations.json
// content bytes R, the digest D_R = sha256(R), and the attestation bundle bytes
// for D_R — all fetched in the SAME origin batch and expiring at the SAME
// instant. Serving R guarantees /attestations/sha256:D_R hits THIS bundle.
type revocationUnit struct {
	content   []byte // revocations.json bytes (R)
	digestHex string // hex sha256(R) = D_R
	attBytes  []byte // attestation bundle bytes for D_R
	expires   time.Time
	// negative is set when the upstream revocations asset itself 404s. The
	// daemon treats a missing revocations asset as fail-open (pickEffectiveList
	// uses persisted), so a 404 here is a legitimate, short-cacheable state.
	negative bool
}

func (u *revocationUnit) fresh() bool { return time.Now().Before(u.expires) }

// mirrorCache is the three-bucket lazy cache-through with single-flight.
type mirrorCache struct {
	origin mirrorOriginFetcher

	pointerTTL    time.Duration
	releaseTagTTL time.Duration
	revocationTTL time.Duration
	negativeTTL   time.Duration

	// single-flight group. The key is ALWAYS the cache key for the value being
	// produced (review suggestion: single-flight key == cache key) so a
	// converged origin fetch writes exactly one cache entry — no cross-key
	// blocking, no two-keys-one-fetch mismatch.
	sf singleflight.Group

	mu sync.Mutex
	// bucket ① immutable, content-addressed. Key forms:
	//   "att:"   + digestHex      → binary-artifact attestation bundle bytes
	//   "asset:" + tag + "/" + name → archive / checksums bytes
	immutable map[string]*immutableEntry
	// immutableLRU tracks recency for capacity eviction (newest at tail).
	immutableLRU []string

	// bucket ② pointer.
	latest ttlEntry

	// per-tag release metadata resident cache (pointer-bucket shape: TTL +
	// serve-stale, keyed by tag). Backs both MirrorReleaseByTag and the MirrorAsset
	// SSRF whitelist so a fleet resolving the same tag costs ONE upstream metadata
	// call per version. A 404 (tag not yet published) is held as a short-TTL
	// negative (releaseByTag[tag].negative) to blunt random-tag probing.
	releaseByTag map[string]*ttlEntry
	// releaseByTagLRU tracks recency for capacity eviction (newest at tail), the
	// SAME parallel-slice范式 as immutableLRU. The tag key is attacker-controlled
	// (URL path) and BOTH positive and negative entries are inserted, so this bound
	// is what prevents an unbounded-growth OOM from random-tag probing.
	releaseByTagLRU []string

	// bucket ③ revocations atomic unit (single deployment-wide table).
	revocation *revocationUnit
}

func newMirrorCache(origin mirrorOriginFetcher) *mirrorCache {
	return &mirrorCache{
		origin:        origin,
		pointerTTL:    mirrorPointerTTL,
		releaseTagTTL: mirrorReleaseTagTTL,
		revocationTTL: mirrorRevocationTTL,
		negativeTTL:   mirrorNegativeTTL,
		immutable:     make(map[string]*immutableEntry),
		releaseByTag:  make(map[string]*ttlEntry),
	}
}

// ---- bucket ② : /releases/latest (60s TTL + serve-stale) -------------------

// getLatest returns the cached latest-release metadata, refreshing on a cold or
// expired entry. On upstream transport failure it serve-stales the last good
// value (INV-20 ②); only a cold miss surfaces the error.
func (c *mirrorCache) getLatest() (*cli.GitHubRelease, error) {
	c.mu.Lock()
	if c.latest.release != nil && c.latest.fresh() {
		rel := c.latest.release
		c.mu.Unlock()
		return rel, nil
	}
	haveStale := c.latest.release != nil
	stale := c.latest.release
	c.mu.Unlock()

	// The refresh is single-flight'd (refreshLatest → c.sf.Do("latest")): N
	// concurrent expired-entry readers COALESCE onto ONE origin fetch, the
	// followers block until it returns. This is the deliberate choice — it caps
	// the server→GitHub fan-out at one in-flight call per key (the herd-root-cause
	// goal), at the cost of briefly blocking followers behind a slow origin. We do
	// NOT detach the refresh (DoChan + return-stale-immediately): the metadata
	// fetch is small and 10s-bounded, and blocking is what guarantees exactly one
	// upstream call. On a transient origin error with a stale value in hand we
	// keep serving stale (INV-20 ②); only a cold miss surfaces the error.
	if haveStale {
		rel, err := c.refreshLatest()
		if err != nil {
			return stale, nil
		}
		return rel, nil
	}
	return c.refreshLatest()
}

func (c *mirrorCache) refreshLatest() (*cli.GitHubRelease, error) {
	v, err, _ := c.sf.Do("latest", func() (interface{}, error) {
		rel, err := c.origin.fetchLatestRelease()
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.latest = ttlEntry{release: rel, expires: time.Now().Add(c.pointerTTL)}
		c.mu.Unlock()
		return rel, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*cli.GitHubRelease), nil
}

// ---- release-by-tag metadata (used by MirrorReleaseByTag + SSRF whitelist) --

// getReleaseByTag resolves a tag's authoritative release metadata with a
// resident per-tag cache (TTL + serve-stale, pointer-bucket shape). Release
// metadata is effectively immutable once published, so a 60s TTL is ample; the
// short TTL (rather than treating it as truly-immutable) still lets a
// not-yet-fully-published tag re-resolve.
//
// This closes the review's getReleaseByTag hot path: the metadata path is hit on
// EVERY daemon's release-by-tag and EVERY MirrorAsset SSRF-whitelist resolution.
// Without a resident cache, single-flight only collapses requests that overlap
// the brief origin-fetch window, so a fleet spread over time re-origined the
// metadata N times (callsReleaseByTag == N) — the herd merely moved from the
// daemons to the server while GitHub's metadata API stayed punched through. With
// the resident cache, N daemons resolving the same tag cost GitHub ONE upstream
// metadata call per version (callsReleaseByTag == 1 within the TTL).
//
// 404≠504 (same server-cache-correctness口径 as getImmutableBytes / getAttestation,
// INV-18): an upstream 404 (tag objectively not published) is cached as a
// short-TTL negative to blunt random-tag probing on the public endpoint; a 504 /
// timeout / transport error is NEVER cached — it serve-stales the last good
// metadata or surfaces the error so the next request re-origins. This is a server
// property only; the daemon treats any non-200 uniformly (INV-16).
func (c *mirrorCache) getReleaseByTag(tag string) (*cli.GitHubRelease, error) {
	c.mu.Lock()
	if e, ok := c.releaseByTag[tag]; ok && e.fresh() {
		// Touch recency on a fresh hit (positive OR negative) so the LRU bound
		// evicts genuinely-cold tags, not actively-resolved ones — same as the
		// immutable bucket's read-hit touch.
		c.releaseByTagLRU = touchLRU(c.releaseByTagLRU, tag)
		if e.negative {
			c.mu.Unlock()
			return nil, &cli.MirrorNotFound{What: "release " + tag}
		}
		rel := e.release
		c.mu.Unlock()
		return rel, nil
	}
	// Capture any stale positive entry for serve-stale on a transient origin error.
	var stale *cli.GitHubRelease
	if e, ok := c.releaseByTag[tag]; ok && !e.negative {
		stale = e.release
	}
	c.mu.Unlock()

	key := "rel:" + tag
	v, err, _ := c.sf.Do(key, func() (interface{}, error) {
		rel, ferr := c.origin.fetchReleaseByTag(tag)
		if ferr != nil {
			if cli.IsMirrorNotFound(ferr) {
				// Cacheable short-TTL negative: tag objectively not published.
				// Bounded by the SAME LRU as positives — attacker-controlled
				// random tags cannot pin unbounded negatives.
				c.mu.Lock()
				c.putReleaseTagLocked(tag, &ttlEntry{negative: true, expires: time.Now().Add(c.negativeTTL)})
				c.mu.Unlock()
			}
			// Transient errors are NOT cached (INV-18): next request re-origins.
			return nil, ferr
		}
		c.mu.Lock()
		c.putReleaseTagLocked(tag, &ttlEntry{release: rel, expires: time.Now().Add(c.releaseTagTTL)})
		c.mu.Unlock()
		return rel, nil
	})
	if err != nil {
		// serve-stale a verified-good positive on a transient origin failure; a
		// cold miss (or an authoritative 404) surfaces the error.
		if stale != nil && !cli.IsMirrorNotFound(err) {
			return stale, nil
		}
		return nil, err
	}
	return v.(*cli.GitHubRelease), nil
}

// ---- bucket ① : attestation bundles (immutable, content-addressed) ---------

// getAttestation returns the raw attestation bundle bytes for digestHex from the
// immutable bucket, origin-fetching on a miss. Bytes are byte-for-byte the
// GitHub originals (INV-18 dumb pass-through). Returns (nil, cli.MirrorNotFound)
// for a cacheable upstream 404; a transient error is NOT cached.
//
// digestHex must be 64 lowercase hex (the handler validates before calling).
func (c *mirrorCache) getAttestation(digestHex string) ([]byte, error) {
	key := "att:" + digestHex
	return c.getImmutableBytes(key, func() ([]byte, error) {
		return c.origin.fetchAttestation(digestHex)
	})
}

// getAsset returns archive/checksums bytes for (tag, assetName) from the
// immutable bucket. downloadURL is the GitHub-authoritative URL the caller
// already SSRF-whitelisted (INV-21a). The cache key is the full (tag, asset)
// pair (review suggestion:防 cross-tag同名 asset 串味), independent of the URL.
func (c *mirrorCache) getAsset(tag, assetName, downloadURL string) ([]byte, error) {
	key := "asset:" + tag + "/" + assetName
	return c.getImmutableBytes(key, func() ([]byte, error) {
		return c.origin.fetchAsset(downloadURL)
	})
}

// getImmutableBytes is the shared immutable-bucket cache-through. single-flight
// key == cache key. Positive entries never expire (LRU eviction only). A 404
// negative result is cached with the short negative TTL; a transient error is
// not cached.
func (c *mirrorCache) getImmutableBytes(key string, fetch func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if e, ok := c.immutable[key]; ok {
		if e.negative {
			if time.Now().Before(e.negExpires) {
				c.touchLRULocked(key)
				c.mu.Unlock()
				return nil, &cli.MirrorNotFound{What: key}
			}
			// negative expired → fall through to re-origin.
		} else {
			c.touchLRULocked(key)
			b := e.bytes
			c.mu.Unlock()
			return b, nil
		}
	}
	c.mu.Unlock()

	v, err, _ := c.sf.Do(key, func() (interface{}, error) {
		b, ferr := fetch()
		if ferr != nil {
			if cli.IsMirrorNotFound(ferr) {
				// Cacheable negative (short TTL): authoritative "does not exist".
				c.mu.Lock()
				c.putImmutableLocked(key, &immutableEntry{
					negative:   true,
					negExpires: time.Now().Add(c.negativeTTL),
				})
				c.mu.Unlock()
			}
			// Transient errors (504/timeout/conn) are NOT cached: next request
			// re-origins (INV-18).
			return nil, ferr
		}
		c.mu.Lock()
		c.putImmutableLocked(key, &immutableEntry{bytes: b})
		c.mu.Unlock()
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (c *mirrorCache) putImmutableLocked(key string, e *immutableEntry) {
	if _, exists := c.immutable[key]; !exists {
		c.immutableLRU = append(c.immutableLRU, key)
	} else {
		c.immutableLRU = touchLRU(c.immutableLRU, key)
	}
	c.immutable[key] = e
	c.evictImmutableLocked()
}

func (c *mirrorCache) touchLRULocked(key string) {
	c.immutableLRU = touchLRU(c.immutableLRU, key)
}

func (c *mirrorCache) evictImmutableLocked() {
	for len(c.immutableLRU) > mirrorImmutableMaxEntries {
		oldest := c.immutableLRU[0]
		c.immutableLRU = c.immutableLRU[1:]
		delete(c.immutable, oldest)
	}
}

// putReleaseTagLocked inserts/refreshes a releaseByTag entry under c.mu, keeping
// releaseByTagLRU in recency order and evicting the oldest past
// mirrorReleaseTagMaxEntries. Reuses the SAME LRU范式 as the immutable bucket
// (touchLRU helper) so positive (:297) and negative (:290) writes share ONE
// bound — neither can grow unbounded under attacker-controlled random tags.
func (c *mirrorCache) putReleaseTagLocked(tag string, e *ttlEntry) {
	if _, exists := c.releaseByTag[tag]; !exists {
		c.releaseByTagLRU = append(c.releaseByTagLRU, tag)
	} else {
		c.releaseByTagLRU = touchLRU(c.releaseByTagLRU, tag)
	}
	c.releaseByTag[tag] = e
	for len(c.releaseByTagLRU) > mirrorReleaseTagMaxEntries {
		oldest := c.releaseByTagLRU[0]
		c.releaseByTagLRU = c.releaseByTagLRU[1:]
		delete(c.releaseByTag, oldest)
	}
}

// touchLRU moves key to the tail (most-recently-used) of the recency slice,
// removing any prior occurrence. Shared by the immutable and releaseByTag
// buckets so both evict with identical, consistent semantics.
func touchLRU(lru []string, key string) []string {
	for i, k := range lru {
		if k == key {
			lru = append(lru[:i], lru[i+1:]...)
			break
		}
	}
	return append(lru, key)
}

// ---- bucket ③ : revocations atomic unit (content + its attestation) --------

// getRevocationUnit returns the cached revocations atomic unit, refreshing on a
// cold/expired entry. Like getLatest, the refresh COALESCES onto one
// single-flight'd origin batch (followers block until it returns) — the
// deliberate one-upstream-call-per-key choice, NOT a detached background
// refresh. On upstream transport failure it serve-stales the last good unit
// (INV-20 ③); only a cold miss surfaces the error.
//
// The unit fetches the revocations.json content R and its attestation for
// D_R=sha256(R) in the SAME single-flight'd batch and binds them to one expiry,
// so a later /attestations/sha256:D_R request always hits the bundle that
// matches the R the mirror is currently serving (BLOCKER-2 — no digest mismatch
// → no吊销门 fail-open from a stale-content / fresh-attestation split).
func (c *mirrorCache) getRevocationUnit(tag, assetName, downloadURL string) (*revocationUnit, error) {
	c.mu.Lock()
	if c.revocation != nil && c.revocation.fresh() {
		u := c.revocation
		c.mu.Unlock()
		return u, nil
	}
	stale := c.revocation
	c.mu.Unlock()

	v, err, _ := c.sf.Do("revocation-unit", func() (interface{}, error) {
		// Fetch R first.
		content, ferr := c.origin.fetchAsset(downloadURL)
		if ferr != nil {
			if cli.IsMirrorNotFound(ferr) {
				neg := &revocationUnit{negative: true, expires: time.Now().Add(c.negativeTTL)}
				c.mu.Lock()
				c.revocation = neg
				c.mu.Unlock()
				return neg, nil
			}
			return nil, ferr // transient: not cached
		}
		sum := sha256.Sum256(content)
		digestHex := hex.EncodeToString(sum[:])

		// Pre-warm the matching attestation from the SAME batch.
		attBytes, aerr := c.origin.fetchAttestation(digestHex)
		if aerr != nil {
			// 404 here means the rev table is published but its attestation
			// isn't (or is gone). Cache the unit's content with a NIL attestation
			// short-TTL'd: the daemon will fail to verify (fail-open via
			// persisted), and we re-origin soon. A transient attestation error is
			// NOT cached at all.
			if cli.IsMirrorNotFound(aerr) {
				unit := &revocationUnit{
					content:   content,
					digestHex: digestHex,
					attBytes:  nil,
					expires:   time.Now().Add(c.revocationTTL),
				}
				c.mu.Lock()
				c.revocation = unit
				// Also expose the (negative) attestation through the immutable
				// path so /attestations/sha256:D_R reflects the same batch.
				c.putImmutableLocked("att:"+digestHex, &immutableEntry{
					negative:   true,
					negExpires: time.Now().Add(c.revocationTTL),
				})
				c.mu.Unlock()
				return unit, nil
			}
			return nil, aerr // transient: not cached
		}

		unit := &revocationUnit{
			content:   content,
			digestHex: digestHex,
			attBytes:  attBytes,
			expires:   time.Now().Add(c.revocationTTL),
		}
		c.mu.Lock()
		c.revocation = unit
		// Bind D_R's attestation into the immutable/att path keyed by D_R so the
		// daemon's independent GET /attestations/sha256:D_R hits THIS same-batch
		// bundle (BLOCKER-2). It shares D_R's content addressing; if the rev
		// table later re-signs to D_new, the new batch writes att:D_new and this
		// unit's expiry rolls both content and attestation together.
		c.putImmutableLocked("att:"+digestHex, &immutableEntry{bytes: attBytes})
		c.mu.Unlock()
		return unit, nil
	})
	if err != nil {
		// serve-stale on transient origin failure.
		if stale != nil {
			return stale, nil
		}
		return nil, err
	}
	return v.(*revocationUnit), nil
}
