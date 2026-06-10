package backend

import "testing"

func TestAssessSpectralQualityFlagsUpscaled320MP3(t *testing.T) {
	spectrum := testSpectrumWithCutoff(44100, 16000)

	assessment := assessSpectralQuality("mp3", 320, spectrum)

	if assessment.VerdictCode != "suspicious" {
		t.Fatalf("expected suspicious verdict, got %q", assessment.VerdictCode)
	}
	if assessment.EstimatedBitrateKbps != 128 {
		t.Fatalf("expected estimated bitrate 128, got %d", assessment.EstimatedBitrateKbps)
	}
	if assessment.Confidence < 0.75 {
		t.Fatalf("expected high confidence for clear 128->320 mismatch, got %.2f", assessment.Confidence)
	}
}

func TestAssessSpectralQualityAcceptsWideband320MP3(t *testing.T) {
	spectrum := testSpectrumWithCutoff(44100, 20500)

	assessment := assessSpectralQuality("mp3", 320, spectrum)

	if assessment.VerdictCode != "likely_genuine" {
		t.Fatalf("expected likely genuine verdict, got %q", assessment.VerdictCode)
	}
	if assessment.EstimatedBitrateKbps < 256 {
		t.Fatalf("expected estimated bitrate at least 256, got %d", assessment.EstimatedBitrateKbps)
	}
	if assessment.CutoffFrequencyHz < 20000 {
		t.Fatalf("expected cutoff near 20 kHz, got %.0f Hz", assessment.CutoffFrequencyHz)
	}
}

func TestAssessSpectralQualityMarksMidBand320MP3Inconclusive(t *testing.T) {
	spectrum := testSpectrumWithCutoff(44100, 18400)

	assessment := assessSpectralQuality("mp3", 320, spectrum)

	if assessment.VerdictCode != "inconclusive" {
		t.Fatalf("expected inconclusive verdict, got %q", assessment.VerdictCode)
	}
	if assessment.EstimatedBitrateKbps != 192 {
		t.Fatalf("expected estimated bitrate 192, got %d", assessment.EstimatedBitrateKbps)
	}
}

func TestAssessSpectralQualityFlagsLimitedFLACSpectrum(t *testing.T) {
	spectrum := testSpectrumWithCutoff(44100, 16000)

	assessment := assessSpectralQuality("flac", 0, spectrum)

	if assessment.VerdictCode != "spectrum_limited" {
		t.Fatalf("expected spectrum-limited FLAC verdict, got %q", assessment.VerdictCode)
	}
	if assessment.EstimatedBitrateKbps != 128 {
		t.Fatalf("expected estimated source tier 128, got %d", assessment.EstimatedBitrateKbps)
	}
}

func testSpectrumWithCutoff(sampleRate int, cutoffHz float64) *SpectrumData {
	const bins = 512
	maxFreq := float64(sampleRate) / 2
	magnitudes := make([]float64, bins)
	for i := range magnitudes {
		freq := float64(i) * maxFreq / float64(bins-1)
		if freq <= cutoffHz {
			magnitudes[i] = -20
		} else {
			magnitudes[i] = -95
		}
	}

	return &SpectrumData{
		TimeSlices: []TimeSlice{
			{
				Time:       0,
				Magnitudes: magnitudes,
			},
		},
		SampleRate: sampleRate,
		FreqBins:   bins,
		Duration:   1,
		MaxFreq:    maxFreq,
	}
}
