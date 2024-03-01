package hacknaam

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/ipfs/boxo/ipns"
	"github.com/ipfs/boxo/path"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-datastore/sync"
	"github.com/ipld/go-ipld-prime"
	_ "github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/linking"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipni/go-libipni/announce"
	"github.com/ipni/go-libipni/announce/httpsender"
	"github.com/ipni/go-libipni/dagsync"
	"github.com/ipni/go-libipni/dagsync/ipnisync"
	"github.com/ipni/go-libipni/ingest/schema"
	"github.com/ipni/go-naam"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multicodec"
	"github.com/multiformats/go-multihash"
	"github.com/multiformats/go-varint"
)

var (
	ls = cidlink.LinkPrototype{
		Prefix: cid.Prefix{
			Version: 1,
			//Codec:    uint64(multicodec.DagCbor),
			Codec:    uint64(multicodec.DagJson),
			MhType:   uint64(multicodec.Sha2_256),
			MhLength: -1,
		},
	}

	headAdCid = datastore.NewKey("headAdCid")
	height    = datastore.NewKey("height")
)

type Naam struct {
	ds            datastore.Datastore
	h             host.Host
	httpAnnouncer *httpsender.Sender
	ls            *ipld.LinkSystem
	pub           dagsync.Publisher
}

// New creates a new Naam instance for publishing IPNS records in an indexer.
func New(httpListenAddr, httpIndexerURL string) (*Naam, error) {
	h, err := libp2p.New()
	if err != nil {
		return nil, err
	}

	ds := sync.MutexWrap(datastore.NewMapDatastore())
	linkSys := makeLinkSys(ds)

	// Create publisher that publishes over http and libptp.
	pk := h.Peerstore().PrivKey(h.ID())
	pub, err := ipnisync.NewPublisher(*linkSys, pk,
		ipnisync.WithHTTPListenAddrs(httpListenAddr),
		ipnisync.WithStreamHost(h),
	)
	if err != nil {
		return nil, err
	}

	indexerURL, err := url.Parse(httpIndexerURL)
	if err != nil {
		return nil, err
	}

	// Create a direct http announcement sender.
	httpAnnouncer, err := httpsender.New([]*url.URL{indexerURL}, h.ID())
	if err != nil {
		return nil, err
	}

	return &Naam{
		ds:            ds,
		h:             h,
		httpAnnouncer: httpAnnouncer,
		ls:            linkSys,
		pub:           pub,
	}, nil
}

func makeLinkSys(ds datastore.Datastore) *ipld.LinkSystem {
	linkSys := cidlink.DefaultLinkSystem()
	ds = namespace.Wrap(ds, datastore.NewKey("ls"))
	linkSys.StorageReadOpener = func(ctx linking.LinkContext, l datamodel.Link) (io.Reader, error) {
		val, err := ds.Get(ctx.Ctx, datastore.NewKey(l.Binary()))
		if err != nil {
			return nil, err
		}
		return bytes.NewBuffer(val), nil
	}
	linkSys.StorageWriteOpener = func(ctx ipld.LinkContext) (io.Writer, ipld.BlockWriteCommitter, error) {
		buf := bytes.NewBuffer(nil)
		return buf, func(l ipld.Link) error {
			return ds.Put(ctx.Ctx, datastore.NewKey(l.Binary()), buf.Bytes())
		}, nil
	}
	return &linkSys
}

func Name(peerID peer.ID) string {
	return ipns.NamespacePrefix + peerID.String()
}

func (n *Naam) Name() string {
	return Name(n.h.ID())
}

func peerIDFromName(name string) (peer.ID, error) {
	spid := strings.TrimPrefix(name, ipns.NamespacePrefix)
	if spid == name {
		// Missing `/ipns/` prefix.
		return "", ipns.ErrInvalidName
	}
	return peer.Decode(spid)
}

func (n *Naam) Publish(ctx context.Context, value path.Path, origName string) error {
	var prevLink ipld.Link
	head, err := n.getHeadAdCid(ctx)
	if err != nil {
		return err
	}
	if head != cid.Undef {
		prevLink = cidlink.Link{Cid: head}
	}

	prevHeight, err := n.previousHeight(ctx)
	if err != nil {
		return err
	}
	seq := prevHeight + 1

	pid := n.h.ID()
	pk := n.h.Peerstore().PrivKey(pid)

	eol := time.Now().Add(24 * time.Hour)
	var ttl time.Duration
	ipnsRec, err := ipns.NewRecord(pk, value, seq, eol, ttl, ipns.WithPublicKey(true))
	if err != nil {
		return err
	}

	hackPID, err := peerIDFromName(origName)
	if err != nil {
		return err
	}

	// Store entry block.
	mh, err := multihash.Sum([]byte(hackPID), multihash.SHA2_256, -1)
	if err != nil {
		return err
	}
	chunk := schema.EntryChunk{
		Entries: []multihash.Multihash{mh},
	}
	cn, err := chunk.ToNode()
	if err != nil {
		return err
	}
	entriesLink, err := n.ls.Store(ipld.LinkContext{Ctx: ctx}, ls, cn)
	if err != nil {
		return err
	}

	metadata, err := ipnsMetadata(ipnsRec)
	if err != nil {
		return err
	}
	ad := schema.Advertisement{
		PreviousID: prevLink,
		Provider:   hackPID.String(),
		Addresses:  n.adAddrs(),
		Entries:    entriesLink,
		ContextID:  naam.ContextID,
		Metadata:   metadata,
	}
	if err := ad.Sign(pk); err != nil {
		return err
	}

	adn, err := ad.ToNode()
	if err != nil {
		return err
	}
	adLink, err := n.ls.Store(ipld.LinkContext{Ctx: ctx}, ls, adn)
	if err != nil {
		return err
	}

	newHead := adLink.(cidlink.Link).Cid
	n.pub.SetRoot(newHead)
	if err := n.setHeadAdCid(ctx, newHead); err != nil {
		return err
	}

	err = announce.Send(ctx, newHead, n.pub.Addrs(), n.httpAnnouncer)
	if err != nil {
		return fmt.Errorf("unsuccessful announce: %w", err)
	}
	return nil
}

func (n *Naam) adAddrs() []string {
	pa := n.pub.Addrs()
	adAddrs := make([]string, 0, len(pa))
	for _, a := range pa {
		adAddrs = append(adAddrs, a.String())
	}
	return adAddrs
}

func (n *Naam) setHeadAdCid(ctx context.Context, head cid.Cid) error {
	if err := n.ds.Put(ctx, headAdCid, head.Bytes()); err != nil {
		return err
	}
	h, err := n.previousHeight(ctx)
	if err != nil {
		return err
	}
	return n.ds.Put(ctx, height, varint.ToUvarint(h+1))
}

func (n *Naam) previousHeight(ctx context.Context) (uint64, error) {
	v, err := n.ds.Get(ctx, height)
	if err != nil {
		if err == datastore.ErrNotFound {
			return 0, nil
		}
		return 0, err
	}
	buf := bytes.NewBuffer(v)
	return varint.ReadUvarint(buf)
}

func (n *Naam) getHeadAdCid(ctx context.Context) (cid.Cid, error) {
	c, err := n.ds.Get(ctx, headAdCid)
	if err != nil {
		if err == datastore.ErrNotFound {
			return cid.Undef, nil
		}
		return cid.Undef, err
	}
	_, head, err := cid.CidFromBytes(c)
	if err != nil {
		return cid.Undef, nil
	}
	return head, nil
}

func ipnsMetadata(rec *ipns.Record) ([]byte, error) {
	var metadata bytes.Buffer
	metadata.Write(varint.ToUvarint(uint64(naam.MetadataProtocolID)))
	marshal, err := ipns.MarshalRecord(rec)
	if err != nil {
		return nil, err
	}
	metadata.Write(marshal)
	return metadata.Bytes(), nil
}
