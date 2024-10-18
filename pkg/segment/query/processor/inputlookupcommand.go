// Copyright (c) 2021-2024 SigScalr, Inc.
//
// This file is part of SigLens Observability Solution
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package processor

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/siglens/siglens/pkg/config"
	"github.com/siglens/siglens/pkg/segment/query/iqr"
	"github.com/siglens/siglens/pkg/segment/structs"
	"github.com/siglens/siglens/pkg/segment/utils"
	segutils "github.com/siglens/siglens/pkg/segment/utils"
)

type inputlookupProcessor struct {
	options *structs.InputLookup
	eof bool
	qid uint64
}

func checkCSVFormat(filename string) bool {
	return strings.HasSuffix(filename, ".csv") || strings.HasSuffix(filename, ".csv.gz")
}


func createRecord(columnNames []string, record []string) (map[string]utils.CValueEnclosure, error) {
	if len(columnNames) != len(record) {
		return nil, fmt.Errorf("CreateRecord: Column and record lengths are not equal")
	}
	recordMap := make(map[string]utils.CValueEnclosure)
	for i, col := range columnNames {
		recordMap[col] = utils.CValueEnclosure{Dtype: utils.SS_DT_STRING, CVal: record[i]}
	}
	return recordMap, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (p *inputlookupProcessor) Process(inpIqr *iqr.IQR) (*iqr.IQR, error) {
	fmt.Println("Called INPUTLOOKUP PROCESS")
	if inpIqr != nil && !p.options.FirstCommand {
		p.qid = inpIqr.GetQid()
		return inpIqr, nil
	}
	if p.eof {
		return nil, io.EOF
	}

	if p.options == nil {
		return nil, fmt.Errorf("PerformInputLookup: InputLookup is nil")
	}
	filename := p.options.Filename

	if !checkCSVFormat(filename) {
		return nil, fmt.Errorf("PerformInputLookup: Only .csv and .csv.gz formats are currently supported")
	}

	filePath := filepath.Join(config.GetLookupPath(), filename)

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("PerformInputLookup: Error while opening file %v, err: %v", filePath, err)
	}
	defer file.Close()

	var reader *csv.Reader
	if strings.HasSuffix(filename, ".csv.gz") {
		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			return nil, fmt.Errorf("PerformInputLookup: Error while creating gzip reader, err: %v", err)
		}
		defer gzipReader.Close()
		reader = csv.NewReader(gzipReader)
	} else {
		reader = csv.NewReader(file)
	}

	// read columns from first row of csv file
	columnNames, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("PerformInputLookup: Error reading column names, err: %v", err)
	}

	curr := 0
	for curr < int(p.options.Start) {
		_, err := reader.Read()
		if err != nil {
			return nil, fmt.Errorf("PerformInputLookup: Error skipping rows, err: %v", err)
		}
		curr++
	}

	count := 0
	records := map[string][]utils.CValueEnclosure{}

	for count < min(int(p.options.Max), int(segutils.QUERY_EARLY_EXIT_LIMIT)) {
		count++
		csvRecord, err := reader.Read()
		if err != nil {
			// Check if we've reached the end of the file
			if err.Error() == "EOF" {
				p.eof = true
				break
			}
			return nil, fmt.Errorf("PerformInputLookup: Error reading record, err: %v", err)
		}

		record, err := createRecord(columnNames, csvRecord)
		if err != nil {
			return nil, fmt.Errorf("PerformInputLookup: Error creating record, err: %v", err)
		}
		if p.options.WhereExpr != nil {
			conditionPassed, err := p.options.WhereExpr.EvaluateForInputLookup(record)
			if err != nil {
				return nil, fmt.Errorf("PerformInputLookup: Error evaluating where expression, err: %v", err)
			}
			if !conditionPassed {
				continue
			}
		}
		for field, CValEnc := range record {
			records[field] = append(records[field], CValEnc)
		}
	}

	if count >= int(p.options.Max) {
		p.eof = true
	}

	nIQR := iqr.NewIQR(p.qid)
	err = nIQR.AppendKnownValues(records)
	if err != nil {
		return nil, fmt.Errorf("PerformInputLookup: Error appending known values, err: %v", err)
	}
	fmt.Println("Processed inputlookup ", len(records))

	return nIQR, nil
}

func (p *inputlookupProcessor) Rewind() {
	panic("not implemented")
}

func (p *inputlookupProcessor) Cleanup() {
	panic("not implemented")
}
