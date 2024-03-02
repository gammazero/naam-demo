package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/gammazero/naam-demo/hacknaam"
	"github.com/ipfs/boxo/path"
	"github.com/ipfs/go-cid"
	"github.com/ipni/go-naam"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// indexer ingest URL (e.g. "https://dev.cid.contact")
	announceURL = "http://localhost:3001"
	// dhstore service URL (e.g. "https://dev.cid.contact")
	findURL = "http://localhost:40080"
	// address:port that publisher listens on.
	httpListenAddr = "0.0.0.0:9999"
	// multiaddr telling the indexer where to fetch advertisements from.
	publisherAddr = "/dns4/localhost/tcp/9999/http"
	// multiaddr to use as provider address in anvertisements.
	providerAddr = "/dns4/ipfs.io/tcp/443/https"

	squirrelCID = "QmPNHBy5h7f19yJDt7ip9TvmMRbqmYsa6aetkrsc1ghjLB"
	radChartCID = "Qmejoony52NYREWv3e9Ap6Uvg29GmJKJpxaDgAbzzYL9kX"
)

func main() {
	err := run(context.TODO())
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	nm, err := naam.New(
		naam.WithListenAddr(httpListenAddr),
		naam.WithAnnounceURL(announceURL),
		naam.WithFindURL(findURL),
		naam.WithPublisherAddrs(publisherAddr),
		naam.WithProviderAddrs(providerAddr),
	)
	if err != nil {
		return fmt.Errorf("failed to create naam: %s", err)
	}

	someCid, err := cid.Decode(squirrelCID)
	if err != nil {
		return err
	}

	pause("create IPNS name to point to CID "+squirrelCID, "This name resolves to a CID that can be changed.")
	ipnsName := nm.Name()
	fmt.Println("IPNS name:", ipnsName)

	publishedPath := path.FromCid(someCid)
	fmt.Println("CID path:", publishedPath)

	pause("publish IPNS record with target CID", "This creates an advertisement that is ingested by IPNI")

	// Publish IPNS record to IPNI indexer.
	err = nm.Publish(ctx, publishedPath, naam.WithEOL(time.Now().Add(48*time.Hour)))
	if err != nil {
		return fmt.Errorf("failed to publish ipns record to ipni: %s", err)
	}
	fmt.Println("IPNS record published with name", ipnsName)

	/*
		// Resolve locally - avoids indexer lookup if naam instance is the publisher.
		resolvedPath, err := nm.Resolve(ctx, ipnsName)
		if err != nil {
			return fmt.Errorf("failed to locally resolve ipns name: %s", err)
		}
		fmt.Println("Resolved IPNS record locally:", ipnsName, "==>", resolvedPath)
	*/

	pause("resolve IPNS name to IPNS record, with reader privacy enabled",
		"This queries IPNI using the IPNS name to lookup the IPNS record. Reader privacy means IPNI is queried using a hash of the IPNS name multihash, and gets back an encrypted response that can be decrypted with the orignal name multihash. This prevents the indexer from knowing what IPNS names a client is resolving.")

retry:
	start := time.Now()

	// Resolve by looking up IPNS record using indexer with reader-privacy.
	resolvedPath, err := naam.Resolve(ctx, ipnsName, findURL)
	if err != nil {
		if errors.Is(err, naam.ErrNotFound) {
			fmt.Println("Name not found on indexer yet, retrying")
			time.Sleep(time.Second)
			goto retry
		}
		return fmt.Errorf("failed to resolve ipns name: %s", err)
	}
	elapsed := time.Since(start)
	fmt.Println("🔒 Reader privacy enabled | Resolved IPNS record using indexer:")
	fmt.Println("    Resolved:", ipnsName, "==>", resolvedPath)
	fmt.Println("    Elapsed:", elapsed)

	pause("fetch data for the CID that was resolved",
		"This example fetches data from IPFS, but the data for the CID can be retrieved from whatever system is appropriate. The point is that it allows a fixed IPNS name to point to data that can be changed only by the owner of the name.")

	openCIDPath(resolvedPath.String())

	pause("publish a replacement IPNS record for a new CID", "")

	someCid, err = cid.Decode(radChartCID)
	if err != nil {
		return err
	}

	fmt.Println("Same IPNS name:", ipnsName)

	publishedPath = path.FromCid(someCid)
	fmt.Println("New CID path:", publishedPath)

	pause("publish IPNS record with new target CID", "This will replace the previous IPNS record")

	// Publish IPNS record to IPNI indexer.
	err = nm.Publish(ctx, publishedPath, naam.WithEOL(time.Now().Add(48*time.Hour)))
	if err != nil {
		return fmt.Errorf("failed to publish ipns record to ipni: %s", err)
	}
	fmt.Println("Updated IPNS record published with name", ipnsName)

	pause("resolve IPNS name to updated IPNS record", "")

	// Resolve by looking up IPNS record using indexer with reader-privacy.
	resolvedPath, err = naam.Resolve(ctx, ipnsName, findURL)
	if err != nil {
		if errors.Is(err, naam.ErrNotFound) {
			fmt.Println("Name not found on indexer yet, retrying")
		}
		return fmt.Errorf("failed to resolve ipns name: %s", err)
	}
	fmt.Println("🔒 Reader privacy enabled | Resolved IPNS record using indexer:")
	fmt.Println("    Resolved:", ipnsName, "==>", resolvedPath)

	pause("fetch data for the CID that was resolved", "")

	openCIDPath(resolvedPath.String())

	pause("resolve IPNS name to IPNS record, without reader privacy", "")

	start = time.Now()

	// Resolve by looking up IPNS record using indexer without reader-privacy.
	resolvedPath, err = naam.ResolveNotPrivate(ctx, ipnsName, findURL)
	if err != nil {
		return fmt.Errorf("failed to resolve ipns name without reader privacy: %s", err)
	}
	elapsed = time.Since(start)
	fmt.Println("⚠️  Reader privacy disabled | Resolved IPNS record using indexer:")
	fmt.Println("    Resolved:", ipnsName, "==>", resolvedPath)
	fmt.Println("    Elapsed:", elapsed)

	pause("try to resolve an IPNS name that is not published", "")

	start = time.Now()

	// Resolve a name that does not have an IPNS record.
	pid, err := peer.Decode("12D3KooWPbQ26UtFJ48ybpCyUoFYFBqH64DbHGMAKtXobKtRdzFF")
	if err != nil {
		return err
	}
	anotherName := naam.Name(pid)
	resolvedPath, err = naam.Resolve(ctx, anotherName, findURL)
	if !errors.Is(err, naam.ErrNotFound) {
		fmt.Errorf("resolver: %s", err)
	}
	elapsed = time.Since(start)
	fmt.Println("Record for unknown name", anotherName, "not found, as expected")
	fmt.Println("    Elapsed:", elapsed)
	fmt.Println()

	pause("create hijacked IPNS record",
		"The hijack publisher does not have the private key that the IPNS name was created with, so cannot create an valid IPNS record corresponding to the name.")

	err = hijackPublishNaam(ctx, ipnsName)
	if err != nil {
		return err
	}

	pause("resolve the hijacked IPNS name",
		"The retrieved record should not validate because it was publisher by someone without the private key associated with the IPNS name. In the future if Naam becomes an official protocol, IPNI will recognize IPNS advertisements and validate the record before ingesting it. This will prevent malicious publishers from blocking IPNS lookup with bad records.")

	// Resolve by looking up IPNS record using indexer with reader-privacy.
	resolvedPath, err = naam.Resolve(ctx, ipnsName, findURL)
	if err != nil {
		if errors.Is(err, naam.ErrNotFound) {
			return fmt.Errorf("name not found on indexer")
		}
		fmt.Println("❌ Failed to resolve ipns name:", err)
	} else {
		fmt.Println("🙀 🔒 Reader privacy enabled | Resolved IPNS record using indexer:")
		fmt.Println("    Resolved:", ipnsName, "==>", resolvedPath)
		return fmt.Errorf("hijacked ipns name resolved")
	}

	return nil
}

func pause(prompt, text string) {
	fmt.Println("\n\n=== Press Enter to", prompt, "===")
	if text != "" {
		fmt.Println(" ", text)
	}
	fmt.Scanln()
}

func hijackPublishNaam(ctx context.Context, ipnsName string) error {
	hackCid, err := cid.Decode("bafybeigvgzoolc3drupxhlevdp2ugqcrbcsqfmcek2zxiw5wctk3xjpjwy")
	if err != nil {
		return err
	}

	hackNaam, err := hacknaam.New("0.0.0.0:9080", announceURL)
	if err != nil {
		return err
	}

	publishedPath := path.FromCid(hackCid)
	fmt.Println("😼 Evil hacker CID path:", publishedPath)

	// Publish IPNS record to IPNI indexer.
	err = hackNaam.Publish(ctx, publishedPath, ipnsName)
	if err != nil {
		return fmt.Errorf("failed to publish ipns record to ipni: %s", err)
	}
	fmt.Println("😼 Evil hacker IPNS record published with same name owned by someone else", ipnsName)
	return nil
}

func openCIDPath(cidPath string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	url := fmt.Sprint("https://ipfs.io" + cidPath)
	//url := fmt.Sprint("https://w3s.link" + cidPath)
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}
