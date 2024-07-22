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

package aggregations

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/siglens/siglens/pkg/common/dtypeutils"
	"github.com/siglens/siglens/pkg/segment/structs"
	"github.com/siglens/siglens/pkg/segment/utils"
)

func ComputeAggEvalForMinOrMax(measureAgg *structs.MeasureAggregator, sstMap map[string]*structs.SegStats, measureResults map[string]utils.CValueEnclosure, isMin bool) error {
	fields := measureAgg.ValueColRequest.GetFields()
	fieldToValue := make(map[string]utils.CValueEnclosure)
	
	if len(fields) == 0 {
		enclosure, exists := measureResults[measureAgg.String()]
		floatValue, strValue, isNumeric, err := GetFloatValueAfterEvaluation(measureAgg, fieldToValue)
		// We cannot compute min/max if constant is not numeric
		// TODO: Perform min/max for strings
		if err != nil {
			return fmt.Errorf("ComputeAggEvalForMinOrMax: Error while evaluating value col request to a numeric value, err: %v", err)
		}
		if !exists {
			enclosure = utils.CValueEnclosure{}
			if isNumeric {
				enclosure.Dtype = utils.SS_DT_FLOAT
				enclosure.CVal = floatValue
			} else {
				enclosure.Dtype = utils.SS_DT_STRING
				enclosure.CVal = strValue
			}
			measureResults[measureAgg.String()] = enclosure
		}
	} else {
		sst, ok := sstMap[fields[0]]
		if !ok {
			return fmt.Errorf("ComputeAggEvalForMinOrMax: applyAggOpOnSegments sstMap was nil for aggCol %v", measureAgg.MeasureCol)
		}

		length := len(sst.Records)
		for i := 0; i < length; i++ {
			enclosure, exists := measureResults[measureAgg.String()]
			
			fieldToValue = make(map[string]utils.CValueEnclosure)
			err := PopulateFieldToValueFromSegStats(fields, measureAgg, sstMap, fieldToValue, i)
			if err != nil {
				return fmt.Errorf("ComputeAggEvalForMinOrMax: Error while populating fieldToValue from sstMap, err: %v", err)
			}
			if measureAgg.ValueColRequest.BooleanExpr != nil {
				boolResult, err := measureAgg.ValueColRequest.BooleanExpr.Evaluate(fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForMinOrMax: there are some errors in the eval function that is inside the %v function: %v", measureAgg.MeasureFunc, err)
				}
				if !exists && boolResult {
					enclosure = utils.CValueEnclosure{
						Dtype: utils.SS_DT_FLOAT,
						CVal:  1.0,
					}
					measureResults[measureAgg.String()] = enclosure
				}
			} else {
				floatValue, strValue, isNumeric, err := GetFloatValueAfterEvaluation(measureAgg, fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForMinOrMax: Error while evaluating value col request, err: %v", err)
				}

				if !exists {
					enclosure = utils.CValueEnclosure{}
					if isNumeric {
						enclosure.Dtype = utils.SS_DT_FLOAT
						enclosure.CVal = floatValue
					} else {
						enclosure.Dtype = utils.SS_DT_STRING
						enclosure.CVal = strValue
					}
					measureResults[measureAgg.String()] = enclosure
				} else {
					currType := enclosure.Dtype
					if currType == utils.SS_DT_STRING {
						// if new value is numeric override the string result
						if isNumeric {
							enclosure.Dtype = utils.SS_DT_FLOAT
							enclosure.CVal = floatValue
						} else {
							strEncValue, isString := enclosure.CVal.(string)
							if !isString {
								return fmt.Errorf("ComputeAggEvalForMinOrMax: String type enclosure does not have a string value")
							}
							if (isMin && strValue < strEncValue) || (!isMin && strValue > strEncValue) {
								enclosure.CVal = strValue
							}
						}
					} else if currType == utils.SS_DT_FLOAT {
						// only check if the current value is numeric
						if isNumeric {
							floatEncValue, isFloat := enclosure.CVal.(float64)
							if !isFloat {
								return fmt.Errorf("ComputeAggEvalForMinOrMax: Float type enclosure does not have a float value")
							}
							if (isMin && floatValue < floatEncValue) || (!isMin && floatValue > floatEncValue) {
								enclosure.CVal = floatValue
							}
						}
					} else {
						return fmt.Errorf("ComputeAggEvalForMinOrMax: Enclosure does not have a valid data type")
					}
					measureResults[measureAgg.String()] = enclosure
				}
			}
		}
	}

	return nil
}

func ComputeAggEvalForRange(measureAgg *structs.MeasureAggregator, sstMap map[string]*structs.SegStats, measureResults map[string]utils.CValueEnclosure, runningEvalStats map[string]interface{}) error {
	fields := measureAgg.ValueColRequest.GetFields()
	fieldToValue := make(map[string]utils.CValueEnclosure)
	maxVal := -1.7976931348623157e+308
	minVal := math.MaxFloat64

	if len(fields) == 0 {
		_, exist := sstMap["*"]
		if !exist {
			return fmt.Errorf("ComputeAggEvalForRange: applyAggOpOnSegments sstMap did not have count when constant was used %v", measureAgg.MeasureCol)
		}
		floatValue, _, isNumeric, err := GetFloatValueAfterEvaluation(measureAgg, fieldToValue)
		// We cannot compute if constant is not numeric
		if err != nil || !isNumeric {
			return fmt.Errorf("ComputeAggEvalForRange: Error while evaluating value col request to a numeric value, err: %v", err)
		}
		maxVal = floatValue
		minVal = floatValue
	} else {
		sst, ok := sstMap[fields[0]]
		if !ok {
			return fmt.Errorf("ComputeAggEvalForRange: applyAggOpOnSegments sstMap was nil for aggCol %v", measureAgg.MeasureCol)
		}

		length := len(sst.Records)
		for i := 0; i < length; i++ {
			fieldToValue = make(map[string]utils.CValueEnclosure)
			err := PopulateFieldToValueFromSegStats(fields, measureAgg, sstMap, fieldToValue, i)
			if err != nil {
				return fmt.Errorf("ComputeAggEvalForRange: Error while populating fieldToValue from sstMap, err: %v", err)
			}

			if measureAgg.ValueColRequest.BooleanExpr != nil {
				boolResult, err := measureAgg.ValueColRequest.BooleanExpr.Evaluate(fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForRange: there are some errors in the eval function that is inside the range function: %v", err)
				}
				if boolResult {
					maxVal = 1
					minVal = 1
				}
			} else {
				floatValue, _, isNumeric, err := GetFloatValueAfterEvaluation(measureAgg, fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForRange: Error while evaluating value col request, err: %v", err)
				}
				// records that are not float will be ignored
				if isNumeric {
					if floatValue < minVal {
						minVal = floatValue
					}
					if floatValue > maxVal {
						maxVal = floatValue
					}
				}
			}
		}
	}

	rangeStat := &structs.RangeStat{}
	rangeStatVal, exists := runningEvalStats[measureAgg.String()]
	if !exists {
		rangeStat.Min = minVal
		rangeStat.Max = maxVal
	} else {
		rangeStat = rangeStatVal.(*structs.RangeStat)
		if rangeStat.Min > minVal {
			rangeStat.Min = minVal
		}
		if rangeStat.Max < maxVal {
			rangeStat.Max = maxVal
		}
	}
	runningEvalStats[measureAgg.String()] = rangeStat
	rangeVal := rangeStat.Max - rangeStat.Min

	enclosure, exists := measureResults[measureAgg.String()]
	if !exists {
		enclosure = utils.CValueEnclosure{
			Dtype: utils.SS_DT_FLOAT,
			CVal:  rangeVal,
		}
		measureResults[measureAgg.String()] = enclosure
	}

	eValFloat, err := enclosure.GetFloatValue()
	if err != nil {
		return fmt.Errorf("ComputeAggEvalForRange: Attempted to perform aggregate range(), but the column %s is not a float value", fields[0])
	}

	if eValFloat < rangeVal {
		enclosure.CVal = rangeVal
		measureResults[measureAgg.String()] = enclosure
	}
	return nil
}

func GetFloatValueAfterEvaluation(measureAgg *structs.MeasureAggregator, fieldToValue map[string]utils.CValueEnclosure) (float64, string, bool, error) {
	valueStr, err := measureAgg.ValueColRequest.EvaluateToString(fieldToValue)
	if err != nil {
		return 0, "", false, fmt.Errorf("GetFloatValueAfterEvaluation: Error while evaluating eval function: %v", err)
	}
	floatVal, err := dtypeutils.ConvertToFloat(valueStr, 64)
	if err != nil {
		return 0, valueStr, false, nil
	}
	return floatVal, valueStr, true, nil
}

func PopulateFieldToValueFromSegStats(fields []string, measureAgg *structs.MeasureAggregator, sstMap map[string]*structs.SegStats, fieldToValue map[string]utils.CValueEnclosure, i int) error {
	for _, field := range fields {
		sst, ok := sstMap[field]
		if !ok {
			return fmt.Errorf("ComputeAggEvalForCount: applyAggOpOnSegments sstMap was nil for aggCol %v", measureAgg.MeasureCol)
		}

		if i >= len(sst.Records) {
			return fmt.Errorf("ComputeAggEvalForCount: Incorrect length of field: %v for aggCol: %v", field, measureAgg.String())
		}
		fieldToValue[field] = *sst.Records[i]
	}

	return nil
}

func ComputeAggEvalForSum(measureAgg *structs.MeasureAggregator, sstMap map[string]*structs.SegStats, measureResults map[string]utils.CValueEnclosure) error {
	fields := measureAgg.ValueColRequest.GetFields()
	sumVal := float64(0)
	fieldToValue := make(map[string]utils.CValueEnclosure)

	if len(fields) == 0 {
		countStat, exist := sstMap["*"]
		if !exist {
			return fmt.Errorf("ComputeAggEvalForSum: applyAggOpOnSegments sstMap did not have count when constant was used %v", measureAgg.MeasureCol)
		}
		floatValue, _, isNumeric, err := GetFloatValueAfterEvaluation(measureAgg, fieldToValue)
		// We cannot compute sum if constant is not numeric
		if err != nil || !isNumeric {
			return fmt.Errorf("ComputeAggEvalForSum: Error while evaluating value col request to a numeric value, err: %v", err)
		}
		sumVal = floatValue * float64(countStat.Count)
	} else {
		sst, ok := sstMap[fields[0]]
		if !ok {
			return fmt.Errorf("ComputeAggEvalForSum: applyAggOpOnSegments sstMap was nil for aggCol %v", measureAgg.MeasureCol)
		}

		length := len(sst.Records)
		for i := 0; i < length; i++ {
			fieldToValue = make(map[string]utils.CValueEnclosure)
			err := PopulateFieldToValueFromSegStats(fields, measureAgg, sstMap, fieldToValue, i)
			if err != nil {
				return fmt.Errorf("ComputeAggEvalForSum: Error while populating fieldToValue from sstMap, err: %v", err)
			}

			if measureAgg.ValueColRequest.BooleanExpr != nil {
				boolResult, err := measureAgg.ValueColRequest.BooleanExpr.Evaluate(fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForSum: there are some errors in the eval function that is inside the sum function: %v", err)
				}
				if boolResult {
					sumVal += 1
				}
			} else {
				floatValue, _, isNumeric, err := GetFloatValueAfterEvaluation(measureAgg, fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForSum: Error while evaluating value col request, err: %v", err)
				}
				// records that are not float will be ignored
				if isNumeric {
					sumVal += floatValue
				}
			}
		}
	}

	enclosure, exists := measureResults[measureAgg.String()]
	if !exists {
		enclosure = utils.CValueEnclosure{
			Dtype: utils.SS_DT_FLOAT,
			CVal:  float64(0),
		}
		measureResults[measureAgg.String()] = enclosure
	}

	eValFloat, err := enclosure.GetFloatValue()
	if err != nil {
		return fmt.Errorf("ComputeAggEvalForSum: Attempted to perform aggregate sum(), but the column %s is not a float value", fields[0])
	}

	enclosure.CVal = eValFloat + sumVal
	measureResults[measureAgg.String()] = enclosure

	return nil
}

func ComputeAggEvalForCount(measureAgg *structs.MeasureAggregator, sstMap map[string]*structs.SegStats, measureResults map[string]utils.CValueEnclosure) error {

	countVal := int64(0)
	fields := measureAgg.ValueColRequest.GetFields()

	if len(fields) == 0 {
		countStat, exist := sstMap["*"]
		if !exist {
			return fmt.Errorf("ComputeAggEvalForCount: applyAggOpOnSegments sstMap did not have count when constant was used %v", measureAgg.MeasureCol)
		}
		countVal = int64(countStat.Count)
	} else {
		sst, ok := sstMap[fields[0]]
		if !ok {
			return fmt.Errorf("ComputeAggEvalForCount: applyAggOpOnSegments sstMap was nil for aggCol %v", measureAgg.MeasureCol)
		}
		length := len(sst.Records)
		if measureAgg.ValueColRequest.BooleanExpr != nil {
			for i := 0; i < length; i++ {
				fieldToValue := make(map[string]utils.CValueEnclosure)
				err := PopulateFieldToValueFromSegStats(fields, measureAgg, sstMap, fieldToValue, i)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForCount: Error while populating fieldToValue from sstMap, err: %v", err)
				}
				boolResult, err := measureAgg.ValueColRequest.BooleanExpr.Evaluate(fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForCount: there are some errors in the eval function that is inside the count function: %v", err)
				}
				if boolResult {
					countVal++
				}
			}
		} else {
			countVal = int64(length)
		}
	}

	enclosure, exists := measureResults[measureAgg.String()]
	if !exists {
		enclosure = utils.CValueEnclosure{
			Dtype: utils.SS_DT_SIGNED_NUM,
			CVal:  int64(0),
		}
		measureResults[measureAgg.String()] = enclosure
	}

	eVal, err := enclosure.GetValue()
	if err != nil {
		return fmt.Errorf("ComputeAggEvalForCount: Attempted to perform aggregate count(), but the column %s is not a float value", fields[0])
	}

	enclosure.CVal = eVal.(int64) + countVal
	measureResults[measureAgg.String()] = enclosure

	return nil
}

func ComputeAggEvalForAvg(measureAgg *structs.MeasureAggregator, sstMap map[string]*structs.SegStats, measureResults map[string]utils.CValueEnclosure, runningEvalStats map[string]interface{}) error {
	fields := measureAgg.ValueColRequest.GetFields()
	fieldToValue := make(map[string]utils.CValueEnclosure)
	sumVal := float64(0)
	countVal := int64(0)

	if len(fields) == 0 {
		countStat, exist := sstMap["*"]
		if !exist {
			return fmt.Errorf("ComputeAggEvalForSum: applyAggOpOnSegments sstMap did not have count when constant was used %v", measureAgg.MeasureCol)
		}
		floatValue, _, isNumeric, err := GetFloatValueAfterEvaluation(measureAgg, fieldToValue)
		// We cannot compute avg if constant is not numeric
		if err != nil || !isNumeric {
			return fmt.Errorf("ComputeAggEvalForSum: Error while evaluating value col request to a numeric value, err: %v", err)
		}
		sumVal = floatValue * float64(countStat.Count)
		countVal = int64(countStat.Count)
	} else {
		sst, ok := sstMap[fields[0]]
		if !ok {
			return fmt.Errorf("ComputeAggEvalForSum: applyAggOpOnSegments sstMap was nil for aggCol %v", measureAgg.MeasureCol)
		}

		length := len(sst.Records)
		for i := 0; i < length; i++ {
			fieldToValue = make(map[string]utils.CValueEnclosure)
			err := PopulateFieldToValueFromSegStats(fields, measureAgg, sstMap, fieldToValue, i)
			if err != nil {
				return fmt.Errorf("ComputeAggEvalForSum: Error while populating fieldToValue from sstMap, err: %v", err)
			}

			if measureAgg.ValueColRequest.BooleanExpr != nil {
				boolResult, err := measureAgg.ValueColRequest.BooleanExpr.Evaluate(fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForSum: there are some errors in the eval function that is inside the avg function: %v", err)
				}
				if boolResult {
					sumVal++
					countVal++
				}
			} else {
				floatValue, _, isNumeric, err := GetFloatValueAfterEvaluation(measureAgg, fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForSum: Error while evaluating value col request, err: %v", err)
				}
				// records that are not float will be ignored
				if isNumeric {
					sumVal += floatValue
					countVal++
				}
			}
		}
	}

	avgStat := &structs.AvgStat{}
	avgStatVal, exists := runningEvalStats[measureAgg.String()]
	if !exists {
		avgStat.Sum = sumVal
		avgStat.Count = countVal
	} else {
		avgStat = avgStatVal.(*structs.AvgStat)
		avgStat.Sum += sumVal
		avgStat.Count += countVal
	}
	runningEvalStats[measureAgg.String()] = avgStat

	measureResults[measureAgg.String()] = utils.CValueEnclosure{
		Dtype: utils.SS_DT_FLOAT,
		CVal:  avgStat.Sum / float64(avgStat.Count),
	}

	return nil
}

func ComputeAggEvalForCardinality(measureAgg *structs.MeasureAggregator, sstMap map[string]*structs.SegStats, measureResults map[string]utils.CValueEnclosure, runningEvalStats map[string]interface{}) error {
	fields := measureAgg.ValueColRequest.GetFields()
	result := 0

	if len(fields) == 0 {
		result = 1
	} else {
		sst, ok := sstMap[fields[0]]
		if !ok {
			return fmt.Errorf("ComputeAggEvalForCardinality: applyAggOpOnSegments sstMap was nil for aggCol %v", measureAgg.MeasureCol)
		}

		strSet := make(map[string]struct{}, 0)
		valuesStrSetVal, exists := runningEvalStats[measureAgg.String()]
		if !exists {
			runningEvalStats[measureAgg.String()] = make(map[string]struct{}, 0)
		} else {
			strSet, ok = valuesStrSetVal.(map[string]struct{})
			if !ok {
				return fmt.Errorf("ComputeAggEvalForCardinality: can not convert strSet for aggCol: %v", measureAgg.String())
			}
		}

		length := len(sst.Records)
		for i := 0; i < length; i++ {
			fieldToValue := make(map[string]utils.CValueEnclosure)
			err := PopulateFieldToValueFromSegStats(fields, measureAgg, sstMap, fieldToValue, i)
			if err != nil {
				return fmt.Errorf("ComputeAggEvalForCardinality: Error while populating fieldToValue from sstMap, err: %v", err)
			}

			if measureAgg.ValueColRequest.BooleanExpr != nil {
				boolResult, err := measureAgg.ValueColRequest.BooleanExpr.Evaluate(fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForCardinality: there are some errors in the eval function that is inside the cardinality function: %v", err)
				}
				if boolResult {
					result = 1
				}
			} else {
				cellValueStr, err := measureAgg.ValueColRequest.EvaluateToString(fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForCardinality: there are some errors in the eval function that is inside the cardinality function: %v", err)
				}
				strSet[cellValueStr] = struct{}{}
				result = len(strSet)
			}	
		}
	}

	measureResults[measureAgg.String()] = utils.CValueEnclosure{
		Dtype: utils.SS_DT_SIGNED_NUM,
		CVal:  int64(result),
	}

	return nil
}

func ComputeAggEvalForValues(measureAgg *structs.MeasureAggregator, sstMap map[string]*structs.SegStats, measureResults map[string]utils.CValueEnclosure, strSet map[string]struct{}) error {
	fields := measureAgg.ValueColRequest.GetFields()
	fieldToValue := make(map[string]utils.CValueEnclosure)
	
	if len(fields) == 0 {
		valueStr, err := measureAgg.ValueColRequest.EvaluateToString(fieldToValue)
		if err != nil {
			return fmt.Errorf("ComputeAggEvalForValues: Error while evaluating value col request function: %v", err)
		}
		strSet[valueStr] = struct{}{}
	} else {
		sst, ok := sstMap[fields[0]]
		if !ok {
			return fmt.Errorf("ComputeAggEvalForValues: applyAggOpOnSegments sstMap was nil for aggCol %v", measureAgg.MeasureCol)
		}

		length := len(sst.Records)
		for i := 0; i < length; i++ {
			fieldToValue := make(map[string]utils.CValueEnclosure)
			err := PopulateFieldToValueFromSegStats(fields, measureAgg, sstMap, fieldToValue, i)
			if err != nil {
				return fmt.Errorf("ComputeAggEvalForValues: Error while populating fieldToValue from sstMap, err: %v", err)
			}

			if measureAgg.ValueColRequest.BooleanExpr != nil {
				boolResult, err := measureAgg.ValueColRequest.BooleanExpr.Evaluate(fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForValues: there are some errors in the eval function that is inside the values function: %v", err)
				}
				if boolResult {
					strSet["1"] = struct{}{}
				}
			} else {
				cellValueStr, err := measureAgg.ValueColRequest.EvaluateToString(fieldToValue)
				if err != nil {
					return fmt.Errorf("ComputeAggEvalForValues: there are some errors in the eval function that is inside the values function: %v", err)
				}
				strSet[cellValueStr] = struct{}{}
			}	
		}
	}

	uniqueStrings := make([]string, 0)
	for str := range strSet {
		uniqueStrings = append(uniqueStrings, str)
	}
	sort.Strings(uniqueStrings)

	strVal := strings.Join(uniqueStrings, "&nbsp")
	measureResults[measureAgg.String()] = utils.CValueEnclosure{
		Dtype: utils.SS_DT_STRING,
		CVal:  strVal,
	}

	return nil
}

func AddMeasureAggInRunningStatsForCount(m *structs.MeasureAggregator, allConvertedMeasureOps *[]*structs.MeasureAggregator, allReverseIndex *[]int, colToIdx map[string][]int, idx int) (int, error) {

	fields := m.ValueColRequest.GetFields()
	if len(fields) == 0 {
		return idx, fmt.Errorf("AddMeasureAggInRunningStatsForCount: Incorrect number of fields for aggCol: %v", m.String())
	}

	// Use the index of agg to map to the corresponding index of the runningStats result, so that we can determine which index of the result set contains the result we need.
	*allReverseIndex = append(*allReverseIndex, idx)
	for _, field := range fields {
		if _, ok := colToIdx[field]; !ok {
			colToIdx[field] = make([]int, 0)
		}
		colToIdx[field] = append(colToIdx[field], idx)
		*allConvertedMeasureOps = append(*allConvertedMeasureOps, &structs.MeasureAggregator{
			MeasureCol:      field,
			MeasureFunc:     utils.Count,
			ValueColRequest: m.ValueColRequest,
			StrEnc:          m.StrEnc,
		})
		idx++
	}
	return idx, nil
}

func AddMeasureAggInRunningStatsForAvg(m *structs.MeasureAggregator, allConvertedMeasureOps *[]*structs.MeasureAggregator, allReverseIndex *[]int, colToIdx map[string][]int, idx int) (int, error) {

	fields := m.ValueColRequest.GetFields()
	if len(fields) != 1 {
		return idx, fmt.Errorf("AddMeasureAggInRunningStatsForAvg: Incorrect number of fields for aggCol: %v", m.String())
	}
	field := fields[0]

	if _, ok := colToIdx[field]; !ok {
		colToIdx[field] = make([]int, 0)
	}

	// We need to use sum() and count() to calculate the avg()
	*allReverseIndex = append(*allReverseIndex, idx)
	colToIdx[field] = append(colToIdx[field], idx)
	*allConvertedMeasureOps = append(*allConvertedMeasureOps, &structs.MeasureAggregator{
		MeasureCol:      field,
		MeasureFunc:     utils.Sum,
		ValueColRequest: m.ValueColRequest,
		StrEnc:          m.StrEnc,
	})
	idx++

	*allReverseIndex = append(*allReverseIndex, idx)
	colToIdx[field] = append(colToIdx[field], idx)
	*allConvertedMeasureOps = append(*allConvertedMeasureOps, &structs.MeasureAggregator{
		MeasureCol:      field,
		MeasureFunc:     utils.Count,
		ValueColRequest: m.ValueColRequest,
		StrEnc:          m.StrEnc,
	})
	idx++
	return idx, nil
}

// Record the index of range() in runningStats; the index is idx
// To calculate the range(), we need both the min() and max(), which require two columns to store them
// Since it is the runningStats not the stats for results, we can use one extra col to store the min/max
// idx stores the result of min, and idx+1 stores the result of max.
func AddMeasureAggInRunningStatsForRange(m *structs.MeasureAggregator, allConvertedMeasureOps *[]*structs.MeasureAggregator, allReverseIndex *[]int, colToIdx map[string][]int, idx int) (int, error) {

	measureCol := m.MeasureCol
	if m.ValueColRequest != nil {
		fields := m.ValueColRequest.GetFields()
		if len(fields) != 1 {
			return idx, fmt.Errorf("AddMeasureAggInRunningStatsForRange: Incorrect number of fields for aggCol: %v", m.String())
		}
		measureCol = fields[0]
	}

	if _, ok := colToIdx[measureCol]; !ok {
		colToIdx[measureCol] = make([]int, 0)
	}
	*allReverseIndex = append(*allReverseIndex, idx)
	colToIdx[measureCol] = append(colToIdx[measureCol], idx)
	*allConvertedMeasureOps = append(*allConvertedMeasureOps, &structs.MeasureAggregator{
		MeasureCol:      measureCol,
		MeasureFunc:     utils.Min,
		ValueColRequest: m.ValueColRequest,
		StrEnc:          m.StrEnc,
	})
	idx++

	*allReverseIndex = append(*allReverseIndex, idx)
	colToIdx[measureCol] = append(colToIdx[measureCol], idx)
	*allConvertedMeasureOps = append(*allConvertedMeasureOps, &structs.MeasureAggregator{
		MeasureCol:      measureCol,
		MeasureFunc:     utils.Max,
		ValueColRequest: m.ValueColRequest,
		StrEnc:          m.StrEnc,
	})
	idx++

	return idx, nil
}

func AddMeasureAggInRunningStatsForValuesOrCardinality(m *structs.MeasureAggregator, allConvertedMeasureOps *[]*structs.MeasureAggregator, allReverseIndex *[]int, colToIdx map[string][]int, idx int) (int, error) {

	fields := m.ValueColRequest.GetFields()
	if len(fields) == 0 {
		return idx, fmt.Errorf("AddMeasureAggInRunningStatsForValuesOrCardinality: Incorrect number of fields for aggCol: %v", m.String())
	}

	// Use the index of agg to map to the corresponding index of the runningStats result, so that we can determine which index of the result set contains the result we need.
	*allReverseIndex = append(*allReverseIndex, idx)
	for _, field := range fields {
		if _, ok := colToIdx[field]; !ok {
			colToIdx[field] = make([]int, 0)
		}
		colToIdx[field] = append(colToIdx[field], idx)
		*allConvertedMeasureOps = append(*allConvertedMeasureOps, &structs.MeasureAggregator{
			MeasureCol:      field,
			MeasureFunc:     utils.Values,
			ValueColRequest: m.ValueColRequest,
			StrEnc:          m.StrEnc,
		})
		idx++
	}
	return idx, nil
}

// Determine if cols used by eval statements or not
func DetermineAggColUsage(measureAgg *structs.MeasureAggregator, aggCols map[string]bool, aggColUsage map[string]utils.AggColUsageMode, valuesUsage map[string]bool) {
	if measureAgg.ValueColRequest != nil {
		for _, field := range measureAgg.ValueColRequest.GetFields() {
			aggCols[field] = true
			colUsage, exists := aggColUsage[field]
			if exists {
				if colUsage == utils.NoEvalUsage {
					aggColUsage[field] = utils.BothUsage
				}
			} else {
				aggColUsage[field] = utils.WithEvalUsage
			}
		}
		if len(aggColUsage) == 0 {
			aggCols["*"] = true
			aggColUsage["*"] = utils.WithEvalUsage
		}
		measureAgg.MeasureCol = measureAgg.StrEnc
	} else {
		aggCols[measureAgg.MeasureCol] = true
		if measureAgg.MeasureFunc == utils.Values {
			valuesUsage[measureAgg.MeasureCol] = true
		}

		colUsage, exists := aggColUsage[measureAgg.MeasureCol]
		if exists {
			if colUsage == utils.WithEvalUsage {
				aggColUsage[measureAgg.MeasureCol] = utils.BothUsage
			}
		} else {
			aggColUsage[measureAgg.MeasureCol] = utils.NoEvalUsage
		}
	}
}
