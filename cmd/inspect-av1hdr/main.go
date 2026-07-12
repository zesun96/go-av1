package main

import (
	"fmt"
	"log"
	"os"

	"github.com/zesun96/go-av1/internal/header"
	"github.com/zesun96/go-av1/internal/obu"
	"github.com/zesun96/go-av1/pkg/ivf"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("usage: inspect-av1hdr <input.ivf>")
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	dmx, err := ivf.NewDemuxer(f, true)
	if err != nil {
		log.Fatal(err)
	}
	_, payload, err := dmx.ReadFrame()
	if err != nil {
		log.Fatal(err)
	}
	obus, _ := obu.SplitOBUs(payload, obu.ParseOptions{})

	var seq *header.SequenceHeader
	for _, o := range obus {
		switch o.Header.Type {
		case header.OBUSequenceHeader:
			var s header.SequenceHeader
			if err := obu.ParseSequenceHeader(o.Payload, &s, obu.ParseOptions{}); err != nil {
				log.Fatal(err)
			}
			seq = &s
			fmt.Printf("seq: sb128=%d filter_intra=%v intra_edge_filter=%v sep_uv_delta_q=%v\n",
				boolInt(seq.SB128), seq.FilterIntra, seq.IntraEdgeFilter, seq.SeparateUVDeltaQ)
		case header.OBUFrame, header.OBUFrameHeader:
			if seq == nil {
				log.Fatal("frame header before sequence header")
			}
			var fh header.FrameHeader
			if _, err := obu.ParseFrameHeaderEx(o.Payload, &fh, obu.FrameParseOptions{SeqHeader: seq}); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("frame: type=%v show=%d base_q=%d qm=%d qmy=%d qmu=%d qmv=%d reduced_txtp=%d delta_q_present=%d delta_q_res=%d lossless=%d skip_mode_allowed=%d\n",
				fh.FrameType, fh.ShowFrame, fh.Quant.YAC, fh.Quant.QM, fh.Quant.QMY, fh.Quant.QMU, fh.Quant.QMV,
				fh.ReducedTxtpSet, fh.Delta.Q.Present, fh.Delta.Q.ResLog2, fh.AllLossless, fh.SkipModeAllowed)
			fmt.Printf("quant_deltas: ydc=%d udc=%d uac=%d vdc=%d vac=%d\n",
				fh.Quant.YDCDelta, fh.Quant.UDCDelta, fh.Quant.UACDelta, fh.Quant.VDCDelta, fh.Quant.VACDelta)
			fmt.Printf("loopfilter: yv=%d yh=%d u=%d v=%d sharp=%d mode_ref=%d ref_delta=%v mode_delta=%v\n",
				fh.LoopFilter.LevelY[0], fh.LoopFilter.LevelY[1], fh.LoopFilter.LevelU, fh.LoopFilter.LevelV,
				fh.LoopFilter.Sharpness, fh.LoopFilter.ModeRefDeltaEnabled,
				fh.LoopFilter.ModeRefDeltas.RefDelta, fh.LoopFilter.ModeRefDeltas.ModeDelta)
			fmt.Printf("delta_lf: present=%d res=%d multi=%d\n",
				fh.Delta.LF.Present, fh.Delta.LF.ResLog2, fh.Delta.LF.Multi)
			for i, q := range fh.Segmentation.QIdx {
				if q != 0 {
					fmt.Printf("seg_qidx[%d]=%d lossless=%d\n", i, q, fh.Segmentation.Lossless[i])
				}
			}
			return
		}
	}
	log.Fatal("no sequence/frame header found in first frame")
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
