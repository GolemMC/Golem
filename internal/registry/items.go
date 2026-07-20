// SPDX-License-Identifier: AGPL-3.0-only

package registry

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

//go:embed data/items_1.21.1.json.gz
var compressedItems []byte

type Item struct {
	ID        int32  `json:"id"`
	Name      string `json:"name"`
	StackSize int32  `json:"stackSize"`
}

var (
	itemsOnce   sync.Once
	itemsByID   map[int32]Item
	itemsByName map[string]Item
	itemsErr    error
)

func ItemByID(id int32) (Item, bool, error) {
	if err := loadItems(); err != nil {
		return Item{}, false, err
	}
	item, ok := itemsByID[id]
	return item, ok, nil
}

func ItemByName(name string) (Item, bool, error) {
	if err := loadItems(); err != nil {
		return Item{}, false, err
	}
	item, ok := itemsByName[strings.TrimPrefix(name, "minecraft:")]
	return item, ok, nil
}

func loadItems() error {
	itemsOnce.Do(func() {
		zr, err := gzip.NewReader(bytes.NewReader(compressedItems))
		if err != nil {
			itemsErr = err
			return
		}
		data, err := io.ReadAll(io.LimitReader(zr, 2<<20))
		closeErr := zr.Close()
		if err != nil {
			itemsErr = err
			return
		}
		if closeErr != nil {
			itemsErr = closeErr
			return
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != ItemFixtureSHA256 {
			itemsErr = fmt.Errorf("embedded item fixture checksum mismatch")
			return
		}
		var items []Item
		if err := json.Unmarshal(data, &items); err != nil {
			itemsErr = err
			return
		}
		itemsByID = make(map[int32]Item, len(items))
		itemsByName = make(map[string]Item, len(items))
		for _, item := range items {
			if item.ID < 0 || item.StackSize < 1 || item.StackSize > 99 {
				itemsErr = fmt.Errorf("invalid item fixture entry %q", item.Name)
				return
			}
			itemsByID[item.ID] = item
			itemsByName[item.Name] = item
		}
		if len(itemsByID) != 1333 {
			itemsErr = fmt.Errorf("item fixture contains %d entries; expected 1333", len(itemsByID))
		}
	})
	return itemsErr
}
