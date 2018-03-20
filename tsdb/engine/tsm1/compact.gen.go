// Generated by tmpl
// https://github.com/benbjohnson/tmpl
//
// DO NOT EDIT!
// Source: compact.gen.go.tmpl

package tsm1

import "sort"

// merge combines the next set of blocks into merged blocks.
func (k *tsmKeyIterator) mergeFloat() {
	// No blocks left, or pending merged values, we're done
	if len(k.blocks) == 0 && len(k.merged) == 0 && len(k.mergedFloatValues) == 0 {
		return
	}

	sort.Stable(k.blocks)

	dedup := len(k.mergedFloatValues) != 0
	if len(k.blocks) > 0 && !dedup {
		// If we have more than one block or any partially tombstoned blocks, we many need to dedup
		dedup = len(k.blocks[0].tombstones) > 0 || k.blocks[0].partiallyRead()

		// Quickly scan each block to see if any overlap with the prior block, if they overlap then
		// we need to dedup as there may be duplicate points now
		for i := 1; !dedup && i < len(k.blocks); i++ {
			dedup = k.blocks[i].partiallyRead() ||
				k.blocks[i].overlapsTimeRange(k.blocks[i-1].minTime, k.blocks[i-1].maxTime) ||
				len(k.blocks[i].tombstones) > 0
		}

	}

	k.merged = k.combineFloat(dedup)
}

// combine returns a new set of blocks using the current blocks in the buffers.  If dedup
// is true, all the blocks will be decoded, dedup and sorted in in order.  If dedup is false,
// only blocks that are smaller than the chunk size will be decoded and combined.
func (k *tsmKeyIterator) combineFloat(dedup bool) blocks {
	if dedup {
		for len(k.mergedFloatValues) < k.size && len(k.blocks) > 0 {
			for len(k.blocks) > 0 && k.blocks[0].read() {
				k.blocks = k.blocks[1:]
			}

			if len(k.blocks) == 0 {
				break
			}
			first := k.blocks[0]
			minTime := first.minTime
			maxTime := first.maxTime

			// Adjust the min time to the start of any overlapping blocks.
			for i := 0; i < len(k.blocks); i++ {
				if k.blocks[i].overlapsTimeRange(minTime, maxTime) && !k.blocks[i].read() {
					if k.blocks[i].minTime < minTime {
						minTime = k.blocks[i].minTime
					}
					if k.blocks[i].maxTime > minTime && k.blocks[i].maxTime < maxTime {
						maxTime = k.blocks[i].maxTime
					}
				}
			}

			// We have some overlapping blocks so decode all, append in order and then dedup
			for i := 0; i < len(k.blocks); i++ {
				if !k.blocks[i].overlapsTimeRange(minTime, maxTime) || k.blocks[i].read() {
					continue
				}

				v, err := DecodeFloatBlock(k.blocks[i].b, &[]FloatValue{})
				if err != nil {
					k.err = err
					return nil
				}

				// Remove values we already read
				v = FloatValues(v).Exclude(k.blocks[i].readMin, k.blocks[i].readMax)

				// Filter out only the values for overlapping block
				v = FloatValues(v).Include(minTime, maxTime)
				if len(v) > 0 {
					// Record that we read a subset of the block
					k.blocks[i].markRead(v[0].UnixNano(), v[len(v)-1].UnixNano())
				}

				// Remove any values filtered globally
				filterMin, filterMax := k.compactionFilter.FilterTimeRange(k.blocks[i].key)
				v = FloatValues(v).Exclude(filterMin, filterMax)

				// Apply each tombstone to the block
				for _, ts := range k.blocks[i].tombstones {
					v = FloatValues(v).Exclude(ts.Min, ts.Max)
				}

				k.mergedFloatValues = k.mergedFloatValues.Merge(v)
			}
		}

		// Since we combined multiple blocks, we could have more values than we should put into
		// a single block.  We need to chunk them up into groups and re-encode them.
		return k.chunkFloat(nil)
	} else {
		var i int

		for i < len(k.blocks) {

			// skip this block if it's values were already read
			if k.blocks[i].read() {
				i++
				continue
			}
			// If we this block is already full, just add it as is
			if BlockCount(k.blocks[i].b) >= k.size {
				k.merged = append(k.merged, k.blocks[i])
			} else {
				break
			}
			i++
		}

		if k.fast {
			for i < len(k.blocks) {
				// skip this block if it's values were already read
				if k.blocks[i].read() {
					i++
					continue
				}

				k.merged = append(k.merged, k.blocks[i])
				i++
			}
		}

		// If we only have 1 blocks left, just append it as is and avoid decoding/recoding
		if i == len(k.blocks)-1 {
			if !k.blocks[i].read() {
				k.merged = append(k.merged, k.blocks[i])
			}
			i++
		}

		// The remaining blocks can be combined and we know that they do not overlap and
		// so we can just append each, sort and re-encode.
		for i < len(k.blocks) && len(k.mergedFloatValues) < k.size {
			if k.blocks[i].read() {
				i++
				continue
			}

			v, err := DecodeFloatBlock(k.blocks[i].b, &[]FloatValue{})
			if err != nil {
				k.err = err
				return nil
			}

			// Apply each tombstone to the block
			for _, ts := range k.blocks[i].tombstones {
				v = FloatValues(v).Exclude(ts.Min, ts.Max)
			}

			k.blocks[i].markRead(k.blocks[i].minTime, k.blocks[i].maxTime)

			k.mergedFloatValues = k.mergedFloatValues.Merge(v)
			i++
		}

		k.blocks = k.blocks[i:]

		return k.chunkFloat(k.merged)
	}
}

func (k *tsmKeyIterator) chunkFloat(dst blocks) blocks {
	if len(k.mergedFloatValues) > k.size {
		values := k.mergedFloatValues[:k.size]
		cb, err := FloatValues(values).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: values[0].UnixNano(),
			maxTime: values[len(values)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedFloatValues = k.mergedFloatValues[k.size:]
		return dst
	}

	// Re-encode the remaining values into the last block
	if len(k.mergedFloatValues) > 0 {
		cb, err := FloatValues(k.mergedFloatValues).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: k.mergedFloatValues[0].UnixNano(),
			maxTime: k.mergedFloatValues[len(k.mergedFloatValues)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedFloatValues = k.mergedFloatValues[:0]
	}
	return dst
}

// merge combines the next set of blocks into merged blocks.
func (k *tsmKeyIterator) mergeInteger() {
	// No blocks left, or pending merged values, we're done
	if len(k.blocks) == 0 && len(k.merged) == 0 && len(k.mergedIntegerValues) == 0 {
		return
	}

	sort.Stable(k.blocks)

	dedup := len(k.mergedIntegerValues) != 0
	if len(k.blocks) > 0 && !dedup {
		// If we have more than one block or any partially tombstoned blocks, we many need to dedup
		dedup = len(k.blocks[0].tombstones) > 0 || k.blocks[0].partiallyRead()

		// Quickly scan each block to see if any overlap with the prior block, if they overlap then
		// we need to dedup as there may be duplicate points now
		for i := 1; !dedup && i < len(k.blocks); i++ {
			dedup = k.blocks[i].partiallyRead() ||
				k.blocks[i].overlapsTimeRange(k.blocks[i-1].minTime, k.blocks[i-1].maxTime) ||
				len(k.blocks[i].tombstones) > 0
		}

	}

	k.merged = k.combineInteger(dedup)
}

// combine returns a new set of blocks using the current blocks in the buffers.  If dedup
// is true, all the blocks will be decoded, dedup and sorted in in order.  If dedup is false,
// only blocks that are smaller than the chunk size will be decoded and combined.
func (k *tsmKeyIterator) combineInteger(dedup bool) blocks {
	if dedup {
		for len(k.mergedIntegerValues) < k.size && len(k.blocks) > 0 {
			for len(k.blocks) > 0 && k.blocks[0].read() {
				k.blocks = k.blocks[1:]
			}

			if len(k.blocks) == 0 {
				break
			}
			first := k.blocks[0]
			minTime := first.minTime
			maxTime := first.maxTime

			// Adjust the min time to the start of any overlapping blocks.
			for i := 0; i < len(k.blocks); i++ {
				if k.blocks[i].overlapsTimeRange(minTime, maxTime) && !k.blocks[i].read() {
					if k.blocks[i].minTime < minTime {
						minTime = k.blocks[i].minTime
					}
					if k.blocks[i].maxTime > minTime && k.blocks[i].maxTime < maxTime {
						maxTime = k.blocks[i].maxTime
					}
				}
			}

			// We have some overlapping blocks so decode all, append in order and then dedup
			for i := 0; i < len(k.blocks); i++ {
				if !k.blocks[i].overlapsTimeRange(minTime, maxTime) || k.blocks[i].read() {
					continue
				}

				v, err := DecodeIntegerBlock(k.blocks[i].b, &[]IntegerValue{})
				if err != nil {
					k.err = err
					return nil
				}

				// Remove values we already read
				v = IntegerValues(v).Exclude(k.blocks[i].readMin, k.blocks[i].readMax)

				// Filter out only the values for overlapping block
				v = IntegerValues(v).Include(minTime, maxTime)
				if len(v) > 0 {
					// Record that we read a subset of the block
					k.blocks[i].markRead(v[0].UnixNano(), v[len(v)-1].UnixNano())
				}

				// Remove any values filtered globally
				filterMin, filterMax := k.compactionFilter.FilterTimeRange(k.blocks[i].key)
				v = IntegerValues(v).Exclude(filterMin, filterMax)

				// Apply each tombstone to the block
				for _, ts := range k.blocks[i].tombstones {
					v = IntegerValues(v).Exclude(ts.Min, ts.Max)
				}

				k.mergedIntegerValues = k.mergedIntegerValues.Merge(v)
			}
		}

		// Since we combined multiple blocks, we could have more values than we should put into
		// a single block.  We need to chunk them up into groups and re-encode them.
		return k.chunkInteger(nil)
	} else {
		var i int

		for i < len(k.blocks) {

			// skip this block if it's values were already read
			if k.blocks[i].read() {
				i++
				continue
			}
			// If we this block is already full, just add it as is
			if BlockCount(k.blocks[i].b) >= k.size {
				k.merged = append(k.merged, k.blocks[i])
			} else {
				break
			}
			i++
		}

		if k.fast {
			for i < len(k.blocks) {
				// skip this block if it's values were already read
				if k.blocks[i].read() {
					i++
					continue
				}

				k.merged = append(k.merged, k.blocks[i])
				i++
			}
		}

		// If we only have 1 blocks left, just append it as is and avoid decoding/recoding
		if i == len(k.blocks)-1 {
			if !k.blocks[i].read() {
				k.merged = append(k.merged, k.blocks[i])
			}
			i++
		}

		// The remaining blocks can be combined and we know that they do not overlap and
		// so we can just append each, sort and re-encode.
		for i < len(k.blocks) && len(k.mergedIntegerValues) < k.size {
			if k.blocks[i].read() {
				i++
				continue
			}

			v, err := DecodeIntegerBlock(k.blocks[i].b, &[]IntegerValue{})
			if err != nil {
				k.err = err
				return nil
			}

			// Apply each tombstone to the block
			for _, ts := range k.blocks[i].tombstones {
				v = IntegerValues(v).Exclude(ts.Min, ts.Max)
			}

			k.blocks[i].markRead(k.blocks[i].minTime, k.blocks[i].maxTime)

			k.mergedIntegerValues = k.mergedIntegerValues.Merge(v)
			i++
		}

		k.blocks = k.blocks[i:]

		return k.chunkInteger(k.merged)
	}
}

func (k *tsmKeyIterator) chunkInteger(dst blocks) blocks {
	if len(k.mergedIntegerValues) > k.size {
		values := k.mergedIntegerValues[:k.size]
		cb, err := IntegerValues(values).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: values[0].UnixNano(),
			maxTime: values[len(values)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedIntegerValues = k.mergedIntegerValues[k.size:]
		return dst
	}

	// Re-encode the remaining values into the last block
	if len(k.mergedIntegerValues) > 0 {
		cb, err := IntegerValues(k.mergedIntegerValues).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: k.mergedIntegerValues[0].UnixNano(),
			maxTime: k.mergedIntegerValues[len(k.mergedIntegerValues)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedIntegerValues = k.mergedIntegerValues[:0]
	}
	return dst
}

// merge combines the next set of blocks into merged blocks.
func (k *tsmKeyIterator) mergeUnsigned() {
	// No blocks left, or pending merged values, we're done
	if len(k.blocks) == 0 && len(k.merged) == 0 && len(k.mergedUnsignedValues) == 0 {
		return
	}

	sort.Stable(k.blocks)

	dedup := len(k.mergedUnsignedValues) != 0
	if len(k.blocks) > 0 && !dedup {
		// If we have more than one block or any partially tombstoned blocks, we many need to dedup
		dedup = len(k.blocks[0].tombstones) > 0 || k.blocks[0].partiallyRead()

		// Quickly scan each block to see if any overlap with the prior block, if they overlap then
		// we need to dedup as there may be duplicate points now
		for i := 1; !dedup && i < len(k.blocks); i++ {
			dedup = k.blocks[i].partiallyRead() ||
				k.blocks[i].overlapsTimeRange(k.blocks[i-1].minTime, k.blocks[i-1].maxTime) ||
				len(k.blocks[i].tombstones) > 0
		}

	}

	k.merged = k.combineUnsigned(dedup)
}

// combine returns a new set of blocks using the current blocks in the buffers.  If dedup
// is true, all the blocks will be decoded, dedup and sorted in in order.  If dedup is false,
// only blocks that are smaller than the chunk size will be decoded and combined.
func (k *tsmKeyIterator) combineUnsigned(dedup bool) blocks {
	if dedup {
		for len(k.mergedUnsignedValues) < k.size && len(k.blocks) > 0 {
			for len(k.blocks) > 0 && k.blocks[0].read() {
				k.blocks = k.blocks[1:]
			}

			if len(k.blocks) == 0 {
				break
			}
			first := k.blocks[0]
			minTime := first.minTime
			maxTime := first.maxTime

			// Adjust the min time to the start of any overlapping blocks.
			for i := 0; i < len(k.blocks); i++ {
				if k.blocks[i].overlapsTimeRange(minTime, maxTime) && !k.blocks[i].read() {
					if k.blocks[i].minTime < minTime {
						minTime = k.blocks[i].minTime
					}
					if k.blocks[i].maxTime > minTime && k.blocks[i].maxTime < maxTime {
						maxTime = k.blocks[i].maxTime
					}
				}
			}

			// We have some overlapping blocks so decode all, append in order and then dedup
			for i := 0; i < len(k.blocks); i++ {
				if !k.blocks[i].overlapsTimeRange(minTime, maxTime) || k.blocks[i].read() {
					continue
				}

				v, err := DecodeUnsignedBlock(k.blocks[i].b, &[]UnsignedValue{})
				if err != nil {
					k.err = err
					return nil
				}

				// Remove values we already read
				v = UnsignedValues(v).Exclude(k.blocks[i].readMin, k.blocks[i].readMax)

				// Filter out only the values for overlapping block
				v = UnsignedValues(v).Include(minTime, maxTime)
				if len(v) > 0 {
					// Record that we read a subset of the block
					k.blocks[i].markRead(v[0].UnixNano(), v[len(v)-1].UnixNano())
				}

				// Remove any values filtered globally
				filterMin, filterMax := k.compactionFilter.FilterTimeRange(k.blocks[i].key)
				v = UnsignedValues(v).Exclude(filterMin, filterMax)

				// Apply each tombstone to the block
				for _, ts := range k.blocks[i].tombstones {
					v = UnsignedValues(v).Exclude(ts.Min, ts.Max)
				}

				k.mergedUnsignedValues = k.mergedUnsignedValues.Merge(v)
			}
		}

		// Since we combined multiple blocks, we could have more values than we should put into
		// a single block.  We need to chunk them up into groups and re-encode them.
		return k.chunkUnsigned(nil)
	} else {
		var i int

		for i < len(k.blocks) {

			// skip this block if it's values were already read
			if k.blocks[i].read() {
				i++
				continue
			}
			// If we this block is already full, just add it as is
			if BlockCount(k.blocks[i].b) >= k.size {
				k.merged = append(k.merged, k.blocks[i])
			} else {
				break
			}
			i++
		}

		if k.fast {
			for i < len(k.blocks) {
				// skip this block if it's values were already read
				if k.blocks[i].read() {
					i++
					continue
				}

				k.merged = append(k.merged, k.blocks[i])
				i++
			}
		}

		// If we only have 1 blocks left, just append it as is and avoid decoding/recoding
		if i == len(k.blocks)-1 {
			if !k.blocks[i].read() {
				k.merged = append(k.merged, k.blocks[i])
			}
			i++
		}

		// The remaining blocks can be combined and we know that they do not overlap and
		// so we can just append each, sort and re-encode.
		for i < len(k.blocks) && len(k.mergedUnsignedValues) < k.size {
			if k.blocks[i].read() {
				i++
				continue
			}

			v, err := DecodeUnsignedBlock(k.blocks[i].b, &[]UnsignedValue{})
			if err != nil {
				k.err = err
				return nil
			}

			// Apply each tombstone to the block
			for _, ts := range k.blocks[i].tombstones {
				v = UnsignedValues(v).Exclude(ts.Min, ts.Max)
			}

			k.blocks[i].markRead(k.blocks[i].minTime, k.blocks[i].maxTime)

			k.mergedUnsignedValues = k.mergedUnsignedValues.Merge(v)
			i++
		}

		k.blocks = k.blocks[i:]

		return k.chunkUnsigned(k.merged)
	}
}

func (k *tsmKeyIterator) chunkUnsigned(dst blocks) blocks {
	if len(k.mergedUnsignedValues) > k.size {
		values := k.mergedUnsignedValues[:k.size]
		cb, err := UnsignedValues(values).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: values[0].UnixNano(),
			maxTime: values[len(values)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedUnsignedValues = k.mergedUnsignedValues[k.size:]
		return dst
	}

	// Re-encode the remaining values into the last block
	if len(k.mergedUnsignedValues) > 0 {
		cb, err := UnsignedValues(k.mergedUnsignedValues).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: k.mergedUnsignedValues[0].UnixNano(),
			maxTime: k.mergedUnsignedValues[len(k.mergedUnsignedValues)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedUnsignedValues = k.mergedUnsignedValues[:0]
	}
	return dst
}

// merge combines the next set of blocks into merged blocks.
func (k *tsmKeyIterator) mergeString() {
	// No blocks left, or pending merged values, we're done
	if len(k.blocks) == 0 && len(k.merged) == 0 && len(k.mergedStringValues) == 0 {
		return
	}

	sort.Stable(k.blocks)

	dedup := len(k.mergedStringValues) != 0
	if len(k.blocks) > 0 && !dedup {
		// If we have more than one block or any partially tombstoned blocks, we many need to dedup
		dedup = len(k.blocks[0].tombstones) > 0 || k.blocks[0].partiallyRead()

		// Quickly scan each block to see if any overlap with the prior block, if they overlap then
		// we need to dedup as there may be duplicate points now
		for i := 1; !dedup && i < len(k.blocks); i++ {
			dedup = k.blocks[i].partiallyRead() ||
				k.blocks[i].overlapsTimeRange(k.blocks[i-1].minTime, k.blocks[i-1].maxTime) ||
				len(k.blocks[i].tombstones) > 0
		}

	}

	k.merged = k.combineString(dedup)
}

// combine returns a new set of blocks using the current blocks in the buffers.  If dedup
// is true, all the blocks will be decoded, dedup and sorted in in order.  If dedup is false,
// only blocks that are smaller than the chunk size will be decoded and combined.
func (k *tsmKeyIterator) combineString(dedup bool) blocks {
	if dedup {
		for len(k.mergedStringValues) < k.size && len(k.blocks) > 0 {
			for len(k.blocks) > 0 && k.blocks[0].read() {
				k.blocks = k.blocks[1:]
			}

			if len(k.blocks) == 0 {
				break
			}
			first := k.blocks[0]
			minTime := first.minTime
			maxTime := first.maxTime

			// Adjust the min time to the start of any overlapping blocks.
			for i := 0; i < len(k.blocks); i++ {
				if k.blocks[i].overlapsTimeRange(minTime, maxTime) && !k.blocks[i].read() {
					if k.blocks[i].minTime < minTime {
						minTime = k.blocks[i].minTime
					}
					if k.blocks[i].maxTime > minTime && k.blocks[i].maxTime < maxTime {
						maxTime = k.blocks[i].maxTime
					}
				}
			}

			// We have some overlapping blocks so decode all, append in order and then dedup
			for i := 0; i < len(k.blocks); i++ {
				if !k.blocks[i].overlapsTimeRange(minTime, maxTime) || k.blocks[i].read() {
					continue
				}

				v, err := DecodeStringBlock(k.blocks[i].b, &[]StringValue{})
				if err != nil {
					k.err = err
					return nil
				}

				// Remove values we already read
				v = StringValues(v).Exclude(k.blocks[i].readMin, k.blocks[i].readMax)

				// Filter out only the values for overlapping block
				v = StringValues(v).Include(minTime, maxTime)
				if len(v) > 0 {
					// Record that we read a subset of the block
					k.blocks[i].markRead(v[0].UnixNano(), v[len(v)-1].UnixNano())
				}

				// Remove any values filtered globally
				filterMin, filterMax := k.compactionFilter.FilterTimeRange(k.blocks[i].key)
				v = StringValues(v).Exclude(filterMin, filterMax)

				// Apply each tombstone to the block
				for _, ts := range k.blocks[i].tombstones {
					v = StringValues(v).Exclude(ts.Min, ts.Max)
				}

				k.mergedStringValues = k.mergedStringValues.Merge(v)
			}
		}

		// Since we combined multiple blocks, we could have more values than we should put into
		// a single block.  We need to chunk them up into groups and re-encode them.
		return k.chunkString(nil)
	} else {
		var i int

		for i < len(k.blocks) {

			// skip this block if it's values were already read
			if k.blocks[i].read() {
				i++
				continue
			}
			// If we this block is already full, just add it as is
			if BlockCount(k.blocks[i].b) >= k.size {
				k.merged = append(k.merged, k.blocks[i])
			} else {
				break
			}
			i++
		}

		if k.fast {
			for i < len(k.blocks) {
				// skip this block if it's values were already read
				if k.blocks[i].read() {
					i++
					continue
				}

				k.merged = append(k.merged, k.blocks[i])
				i++
			}
		}

		// If we only have 1 blocks left, just append it as is and avoid decoding/recoding
		if i == len(k.blocks)-1 {
			if !k.blocks[i].read() {
				k.merged = append(k.merged, k.blocks[i])
			}
			i++
		}

		// The remaining blocks can be combined and we know that they do not overlap and
		// so we can just append each, sort and re-encode.
		for i < len(k.blocks) && len(k.mergedStringValues) < k.size {
			if k.blocks[i].read() {
				i++
				continue
			}

			v, err := DecodeStringBlock(k.blocks[i].b, &[]StringValue{})
			if err != nil {
				k.err = err
				return nil
			}

			// Apply each tombstone to the block
			for _, ts := range k.blocks[i].tombstones {
				v = StringValues(v).Exclude(ts.Min, ts.Max)
			}

			k.blocks[i].markRead(k.blocks[i].minTime, k.blocks[i].maxTime)

			k.mergedStringValues = k.mergedStringValues.Merge(v)
			i++
		}

		k.blocks = k.blocks[i:]

		return k.chunkString(k.merged)
	}
}

func (k *tsmKeyIterator) chunkString(dst blocks) blocks {
	if len(k.mergedStringValues) > k.size {
		values := k.mergedStringValues[:k.size]
		cb, err := StringValues(values).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: values[0].UnixNano(),
			maxTime: values[len(values)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedStringValues = k.mergedStringValues[k.size:]
		return dst
	}

	// Re-encode the remaining values into the last block
	if len(k.mergedStringValues) > 0 {
		cb, err := StringValues(k.mergedStringValues).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: k.mergedStringValues[0].UnixNano(),
			maxTime: k.mergedStringValues[len(k.mergedStringValues)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedStringValues = k.mergedStringValues[:0]
	}
	return dst
}

// merge combines the next set of blocks into merged blocks.
func (k *tsmKeyIterator) mergeBoolean() {
	// No blocks left, or pending merged values, we're done
	if len(k.blocks) == 0 && len(k.merged) == 0 && len(k.mergedBooleanValues) == 0 {
		return
	}

	sort.Stable(k.blocks)

	dedup := len(k.mergedBooleanValues) != 0
	if len(k.blocks) > 0 && !dedup {
		// If we have more than one block or any partially tombstoned blocks, we many need to dedup
		dedup = len(k.blocks[0].tombstones) > 0 || k.blocks[0].partiallyRead()

		// Quickly scan each block to see if any overlap with the prior block, if they overlap then
		// we need to dedup as there may be duplicate points now
		for i := 1; !dedup && i < len(k.blocks); i++ {
			dedup = k.blocks[i].partiallyRead() ||
				k.blocks[i].overlapsTimeRange(k.blocks[i-1].minTime, k.blocks[i-1].maxTime) ||
				len(k.blocks[i].tombstones) > 0
		}

	}

	k.merged = k.combineBoolean(dedup)
}

// combine returns a new set of blocks using the current blocks in the buffers.  If dedup
// is true, all the blocks will be decoded, dedup and sorted in in order.  If dedup is false,
// only blocks that are smaller than the chunk size will be decoded and combined.
func (k *tsmKeyIterator) combineBoolean(dedup bool) blocks {
	if dedup {
		for len(k.mergedBooleanValues) < k.size && len(k.blocks) > 0 {
			for len(k.blocks) > 0 && k.blocks[0].read() {
				k.blocks = k.blocks[1:]
			}

			if len(k.blocks) == 0 {
				break
			}
			first := k.blocks[0]
			minTime := first.minTime
			maxTime := first.maxTime

			// Adjust the min time to the start of any overlapping blocks.
			for i := 0; i < len(k.blocks); i++ {
				if k.blocks[i].overlapsTimeRange(minTime, maxTime) && !k.blocks[i].read() {
					if k.blocks[i].minTime < minTime {
						minTime = k.blocks[i].minTime
					}
					if k.blocks[i].maxTime > minTime && k.blocks[i].maxTime < maxTime {
						maxTime = k.blocks[i].maxTime
					}
				}
			}

			// We have some overlapping blocks so decode all, append in order and then dedup
			for i := 0; i < len(k.blocks); i++ {
				if !k.blocks[i].overlapsTimeRange(minTime, maxTime) || k.blocks[i].read() {
					continue
				}

				v, err := DecodeBooleanBlock(k.blocks[i].b, &[]BooleanValue{})
				if err != nil {
					k.err = err
					return nil
				}

				// Remove values we already read
				v = BooleanValues(v).Exclude(k.blocks[i].readMin, k.blocks[i].readMax)

				// Filter out only the values for overlapping block
				v = BooleanValues(v).Include(minTime, maxTime)
				if len(v) > 0 {
					// Record that we read a subset of the block
					k.blocks[i].markRead(v[0].UnixNano(), v[len(v)-1].UnixNano())
				}

				// Remove any values filtered globally
				filterMin, filterMax := k.compactionFilter.FilterTimeRange(k.blocks[i].key)
				v = BooleanValues(v).Exclude(filterMin, filterMax)

				// Apply each tombstone to the block
				for _, ts := range k.blocks[i].tombstones {
					v = BooleanValues(v).Exclude(ts.Min, ts.Max)
				}

				k.mergedBooleanValues = k.mergedBooleanValues.Merge(v)
			}
		}

		// Since we combined multiple blocks, we could have more values than we should put into
		// a single block.  We need to chunk them up into groups and re-encode them.
		return k.chunkBoolean(nil)
	} else {
		var i int

		for i < len(k.blocks) {

			// skip this block if it's values were already read
			if k.blocks[i].read() {
				i++
				continue
			}
			// If we this block is already full, just add it as is
			if BlockCount(k.blocks[i].b) >= k.size {
				k.merged = append(k.merged, k.blocks[i])
			} else {
				break
			}
			i++
		}

		if k.fast {
			for i < len(k.blocks) {
				// skip this block if it's values were already read
				if k.blocks[i].read() {
					i++
					continue
				}

				k.merged = append(k.merged, k.blocks[i])
				i++
			}
		}

		// If we only have 1 blocks left, just append it as is and avoid decoding/recoding
		if i == len(k.blocks)-1 {
			if !k.blocks[i].read() {
				k.merged = append(k.merged, k.blocks[i])
			}
			i++
		}

		// The remaining blocks can be combined and we know that they do not overlap and
		// so we can just append each, sort and re-encode.
		for i < len(k.blocks) && len(k.mergedBooleanValues) < k.size {
			if k.blocks[i].read() {
				i++
				continue
			}

			v, err := DecodeBooleanBlock(k.blocks[i].b, &[]BooleanValue{})
			if err != nil {
				k.err = err
				return nil
			}

			// Apply each tombstone to the block
			for _, ts := range k.blocks[i].tombstones {
				v = BooleanValues(v).Exclude(ts.Min, ts.Max)
			}

			k.blocks[i].markRead(k.blocks[i].minTime, k.blocks[i].maxTime)

			k.mergedBooleanValues = k.mergedBooleanValues.Merge(v)
			i++
		}

		k.blocks = k.blocks[i:]

		return k.chunkBoolean(k.merged)
	}
}

func (k *tsmKeyIterator) chunkBoolean(dst blocks) blocks {
	if len(k.mergedBooleanValues) > k.size {
		values := k.mergedBooleanValues[:k.size]
		cb, err := BooleanValues(values).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: values[0].UnixNano(),
			maxTime: values[len(values)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedBooleanValues = k.mergedBooleanValues[k.size:]
		return dst
	}

	// Re-encode the remaining values into the last block
	if len(k.mergedBooleanValues) > 0 {
		cb, err := BooleanValues(k.mergedBooleanValues).Encode(nil)
		if err != nil {
			k.err = err
			return nil
		}

		dst = append(dst, &block{
			minTime: k.mergedBooleanValues[0].UnixNano(),
			maxTime: k.mergedBooleanValues[len(k.mergedBooleanValues)-1].UnixNano(),
			key:     k.key,
			b:       cb,
		})
		k.mergedBooleanValues = k.mergedBooleanValues[:0]
	}
	return dst
}
