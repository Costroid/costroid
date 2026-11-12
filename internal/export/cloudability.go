package export

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"time"
)

// Cloudability's FOCUS ingress page (https://www.ibm.com/docs/en/cloudability-commercial/cloudability-essentials/saas?topic=cloudability-connect-custom-data-focus-ingress,
// retrieved 2026-11-12) states that Cloudability "currently supports" FOCUS
// 1.0 and 1.1, that a CSV is compressed with gzip, and that a drop lies at
// "<Root Directory>/<Optional_Sub_Directories>/<Vendor>/<Report_Period>/<Epoch Time Folder>/<prefix>-Manifest.json".
// The report period "must be in the formats YYYYMMDD-YYYYMM(+1)DD", and the
// epoch folder "will be used to identify the latest drop for month-to-date
// data". The manifest's all_report_keys is "the list of only new absolute file
// paths excluding the root directory", and its focus_version "must match the
// exact FOCUS Spec version".
const (
	cloudabilityVendor   = "Costroid"
	cloudabilityPrefix   = "costroid"
	cloudabilityData     = cloudabilityPrefix + "-cost-and-usage.csv.gz"
	cloudabilityManifest = cloudabilityPrefix + "-Manifest.json"
	cloudabilityVersion  = "1.1"
	cloudabilityDay      = "20060102"
)

// cloudabilityManifestFile is the manifest in the order of the page's sample.
// The page's table reads "CSV : gzip" for compression, and its one sample
// writes the value in capitals, "GZIP". report_id is optional and left out.
type cloudabilityManifestFile struct {
	Compression   string   `json:"compression"`
	ContentType   string   `json:"content_type"`
	RootDir       string   `json:"root_dir"`
	AllReportKeys []string `json:"all_report_keys"`
	UpdatedAt     string   `json:"updated_at"`
	FocusVersion  string   `json:"focus_version"`
}

// ErrNoRootDir is a Cloudability export without the bucket its manifest names.
var ErrNoRootDir = errors.New("a Cloudability manifest names its bucket or container, so cloudability needs --root-dir")

// writeCloudability writes one drop for each billing period the rows name:
// the period's rows as FOCUS 1.1 CSV, compressed with gzip, and the manifest
// that names it, in a folder named by the time of the export.
func writeCloudability(rows table, options Options) (Export, error) {
	if options.RootDir == "" {
		return Export{}, ErrNoRootDir
	}
	periods, groups, err := groupRows(rows.rows(), cloudabilityPeriod)
	if err != nil {
		return Export{}, err
	}
	epoch := strconv.FormatInt(options.Now.Unix(), 10)
	var files []File
	for _, period := range periods {
		records := make([][]string, len(groups[period]))
		for i, source := range groups[period] {
			records[i] = project(source, focus11And12Columns, focus11And12Source)
		}
		body, err := csvFile(focus11And12Columns, records)
		if err != nil {
			return Export{}, err
		}
		compressed, err := gzipped(body)
		if err != nil {
			return Export{}, err
		}
		folder := path.Join(cloudabilityVendor, period, epoch)
		data := path.Join(folder, cloudabilityData)
		manifest, err := json.MarshalIndent(cloudabilityManifestFile{
			Compression:   "GZIP",
			ContentType:   "CSV",
			RootDir:       options.RootDir,
			AllReportKeys: []string{data},
			UpdatedAt:     options.Now.UTC().Format(time.RFC3339),
			FocusVersion:  cloudabilityVersion,
		}, "", "  ")
		if err != nil {
			return Export{}, fmt.Errorf("write the Cloudability manifest: %w", err)
		}
		files = append(files,
			File{Name: data, Body: compressed, Rows: len(records)},
			File{Name: path.Join(folder, cloudabilityManifest), Body: append(manifest, '\n'), Note: "Cloudability manifest"})
	}
	return Export{Files: files}, nil
}

// cloudabilityPeriod is a row's report period folder, from its billing
// period's first day to the day its exclusive end falls on. It refuses a row
// whose dates are not UTC instants to the second, the one form the page names.
func cloudabilityPeriod(source row) (string, error) {
	var days [2]string
	for i, column := range []string{"BillingPeriodStart", "BillingPeriodEnd"} {
		day, err := instant(Cloudability, source, column)
		if err != nil {
			return "", err
		}
		days[i] = day.Format(cloudabilityDay)
	}
	for _, column := range []string{"ChargePeriodStart", "ChargePeriodEnd"} {
		if _, err := instant(Cloudability, source, column); err != nil {
			return "", err
		}
	}
	return days[0] + "-" + days[1], nil
}

func gzipped(data []byte) ([]byte, error) {
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(data); err != nil {
		return nil, fmt.Errorf("compress the file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("compress the file: %w", err)
	}
	return out.Bytes(), nil
}
