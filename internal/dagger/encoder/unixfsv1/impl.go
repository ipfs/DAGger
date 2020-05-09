package unixfsv1

import (
	dgrblock "github.com/ipfs-shipyard/DAGger/internal/dagger/block"
	dgrencoder "github.com/ipfs-shipyard/DAGger/internal/dagger/encoder"

	"github.com/ipfs-shipyard/DAGger/internal/dagger/util"
	"github.com/ipfs-shipyard/DAGger/internal/zcpstring"
)

type config struct {
	CompatPb             bool `getopt:"--merkledag-compat-protobuf  Output merkledag links/data in non-canonical protobuf order for convergence with go-ipfs"`
	LegacyCIDv0Links     bool `getopt:"--cidv0                      Generate compat-mode CIDv0 links"`
	NonstandardLeanLinks bool `getopt:"--non-standard-lean-links    Omit dag-size and offset information from all links. While IPFS will likely render the result, ONE VOIDS ALL WARRANTIES"`
	UnixFsType           int  `getopt:"--unixfs-leaf-decorator-type Generate leaves as full UnixFS nodes with the given UnixFSv1 type (0 or 2). When unspecified (default) uses raw leaves instead."`
}

type encoder struct {
	config
	*dgrencoder.DaggerConfig
}

func (e *encoder) NewLeaf(ls dgrblock.LeafSource) *dgrblock.Header {
	if e.UnixFsType == -1 {
		return e.BlockMaker(
			ls.Content,
			dgrblock.CodecRaw,
			uint64(ls.Size),
			0,
			0,
		)

	} else if ls.Size == 0 {
		// short-circuit for convergence with go-ipfs, regardless of UnixFS type id
		return e.compatNulBlock()
	}

	dataLen := util.VarintSlice(uint64(ls.Size))

	blockData := zcpstring.NewWithSegmentCap(9)
	blockData.AddByte(pbHdrF1LD)
	blockData.AddSlice(util.VarintSlice(uint64(3 + 2*len(dataLen) + ls.Size + 1)))
	blockData.AddByte(pbHdrF1VI)
	blockData.AddByte(byte(e.UnixFsType))
	blockData.AddByte(pbHdrF2LD)
	blockData.AddSlice(dataLen)
	blockData.AddZcp(ls.Content)
	blockData.AddByte(pbHdrF3VI)
	blockData.AddSlice(dataLen)

	return e.BlockMaker(
		blockData,
		dgrblock.CodecPB,
		uint64(ls.Size),
		0,
		0,
	)
}

func (e *encoder) NewLink(origin dgrencoder.NodeOrigin, blocks []*dgrblock.Header) *dgrblock.Header {

	var totalPayload, subDagSize uint64
	var linkBlock, linkSection, seekOffsets *zcpstring.ZcpString

	if e.NonstandardLeanLinks {
		seekOffsets = &zcpstring.ZcpString{}
		linkSection = zcpstring.NewWithSegmentCap(5 * len(blocks))
		linkBlock = zcpstring.NewWithSegmentCap(5*len(blocks) + 6)
	} else {
		seekOffsets = zcpstring.NewWithSegmentCap(2 * len(blocks))
		linkSection = zcpstring.NewWithSegmentCap(9 * len(blocks))
		linkBlock = zcpstring.NewWithSegmentCap(9*len(blocks) + 2*len(blocks) + 6)
	}

	for i := range blocks {

		// fmt.Println(blocks[i].CidBase32())

		cid := blocks[i].Cid()
		if e.LegacyCIDv0Links &&
			!blocks[i].IsCidInlined() &&
			blocks[i].SizeCumulativePayload() != blocks[i].SizeCumulativeDag() { // size inequality is a hack to quickly distinguish raw leaf blocks from everything else

			// the magic of CIDv0
			cid = cid[2:]
		}

		cidLenVI := util.VarintSlice(uint64(len(cid)))
		var dagSizeVI []byte
		var frameLen uint64

		if e.NonstandardLeanLinks {
			frameLen = uint64(1 + len(cidLenVI) + len(cid))
		} else {
			dagSizeVI = util.VarintSlice(blocks[i].SizeCumulativeDag())
			frameLen = uint64(1 + len(cidLenVI) + len(cid) + 3 + len(dagSizeVI))
		}

		linkSection.AddByte(pbHdrF2LD)
		linkSection.AddSlice(util.VarintSlice(frameLen))

		linkSection.AddByte(pbHdrF1LD)
		linkSection.AddSlice(cidLenVI)
		linkSection.AddSlice(cid)

		if !e.NonstandardLeanLinks {

			// yes, a zero-length piece needed here for convergence :(((
			linkSection.AddByte(pbHdrF2LD)
			linkSection.AddByte(0)

			linkSection.AddByte(pbHdrF3VI)
			linkSection.AddSlice(dagSizeVI)

			seekOffsets.AddByte(pbHdrF4VI)
			seekOffsets.AddSlice(util.VarintSlice(blocks[i].SizeCumulativePayload()))
		}

		totalPayload += blocks[i].SizeCumulativePayload()
		subDagSize += blocks[i].SizeCumulativeDag()
	}

	linkSectionSize := linkSection.Size()
	payloadSizeVI := util.VarintSlice(totalPayload)

	if e.CompatPb {
		linkBlock.AddZcp(linkSection)
	}

	linkBlock.AddByte(pbHdrF1LD)
	linkBlock.AddSlice(util.VarintSlice(uint64(3 + len(payloadSizeVI) + seekOffsets.Size())))

	linkBlock.AddByte(pbHdrF1VI)
	linkBlock.AddByte(2)
	linkBlock.AddByte(pbHdrF3VI)
	linkBlock.AddSlice(payloadSizeVI)
	linkBlock.AddZcp(seekOffsets)

	if !e.CompatPb {
		linkBlock.AddZcp(linkSection)
	}

	h := e.BlockMaker(
		linkBlock,
		dgrblock.CodecPB,
		totalPayload,
		subDagSize,
		linkSectionSize,
	)

	e.NewLinkBlockCallback(origin, h, nil)

	// fmt.Printf("\t%s\t%d\n", b.CidBase32(), b.SizeCumulativePayload())

	return h
}

func (e *encoder) IpfsCompatibleNulLink(origin dgrencoder.NodeOrigin) *dgrblock.Header {
	h := e.compatNulBlock()
	e.NewLinkBlockCallback(origin, h, nil)
	return h
}

// SANCHECK: do not cache for now... may skew stats
func (e *encoder) compatNulBlock() *dgrblock.Header {
	// represents the protobuf
	// 1 {
	// 	1: 2
	// 	3: 0
	// }
	return e.BlockMaker(
		zcpstring.NewFromSlice([]byte("\x0a\x04\x08\x02\x18\x00")),
		dgrblock.CodecPB,
		0,
		0,
		0,
	)
}

func (e *encoder) LinkframeSize(hdr *dgrblock.Header) int {

	size := len(hdr.Cid())
	size += 1 + util.VarintWireSize(uint64(size))

	if !e.NonstandardLeanLinks {
		size += 3 + util.VarintWireSize(hdr.SizeCumulativeDag())
	}

	return 1 + util.VarintWireSize(uint64(size)) + size
}

const (
	pbHdrF1VI = 0 | ((iota + 1) << 3)
	pbHdrF2VI
	pbHdrF3VI
	pbHdrF4VI
)
const (
	pbHdrF1LD = 2 | ((iota + 1) << 3)
	pbHdrF2LD
	pbHdrF3LD
	pbHdrF4LD
)