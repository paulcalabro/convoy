package blockstores

import (
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/yasker/volmgr/drivers"
	"github.com/yasker/volmgr/metadata"
	"github.com/yasker/volmgr/utils"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	BLOCKSTORE_BASE           = "rancher-blockstore"
	VOLUME_DIRECTORY          = "volume"
	VOLUME_CONFIG_FILE        = "volume.cfg"
	SNAPSHOTS_DIRECTORY       = "snapshots"
	SNAPSHOT_CONFIG_PREFIX    = "snapshot-"
	BLOCKS_DIRECTORY          = "blocks"
	BLOCK_SEPARATE_LAYER1     = 2
	BLOCK_SEPARATE_LAYER2     = 4
	DEFAULT_BLOCK_SIZE        = 2097152
	PRESERVED_CHECKSUM_LENGTH = 64
)

type InitFunc func(configFile, id string, config map[string]string) (BlockStoreDriver, error)

type BlockStoreDriver interface {
	Kind() string
	FileExists(path, fileName string) bool
	FileSize(path, fileName string) int64
	MkDirAll(dirName string) error
	RemoveAll(name string) error
	Read(srcPath, srcFileName string, data []byte) error
	Write(data []byte, dstPath, dstFileName string) error
	CopyToPath(srcFileName string, path string) error
}

type Volume struct {
	Size           uint64
	Base           string
	LastSnapshotId string
}

type BlockStore struct {
	Kind      string
	BlockSize uint32
	Volumes   map[string]Volume
}

type BlockMapping struct {
	Offset uint64
	Block  string
}

type SnapshotMap struct {
	Id     string
	Blocks []BlockMapping
}

var (
	initializers map[string]InitFunc
)

func init() {
	initializers = make(map[string]InitFunc)
}

func RegisterDriver(kind string, initFunc InitFunc) error {
	if _, exists := initializers[kind]; exists {
		return fmt.Errorf("%s has already been registered", kind)
	}
	initializers[kind] = initFunc
	return nil
}

func GetBlockStoreDriver(kind, configFile, id string, config map[string]string) (BlockStoreDriver, error) {
	if _, exists := initializers[kind]; !exists {
		return nil, fmt.Errorf("Driver %v is not supported!", kind)
	}
	return initializers[kind](configFile, id, config)
}

func getDriverConfigFilename(root, kind, id string) string {
	return filepath.Join(root, id+"-"+kind+".cfg")
}

func getConfigFilename(root, id string) string {
	return filepath.Join(root, id+".cfg")
}

func Register(root, kind, id string, config map[string]string) error {
	configFile := getDriverConfigFilename(root, kind, id)
	if _, err := os.Stat(configFile); err == nil {
		return fmt.Errorf("BlockStore %v is already registered", id)
	}
	driver, err := GetBlockStoreDriver(kind, configFile, id, config)
	if err != nil {
		return err
	}
	log.Debug("Created ", configFile)

	basePath := filepath.Join(BLOCKSTORE_BASE, VOLUME_DIRECTORY)
	err = driver.MkDirAll(basePath)
	if err != nil {
		removeDriverConfigFile(root, kind, id)
		return err
	}
	log.Debug("Created base directory of blockstore at ", basePath)

	bs := &BlockStore{
		Kind:      kind,
		Volumes:   make(map[string]Volume),
		BlockSize: DEFAULT_BLOCK_SIZE,
	}
	configFile = getConfigFilename(root, id)
	if err := utils.SaveConfig(configFile, bs); err != nil {
		return err
	}
	log.Debug("Created ", configFile)
	return nil
}

func removeDriverConfigFile(root, kind, id string) error {
	configFile := getDriverConfigFilename(root, kind, id)
	if err := exec.Command("rm", "-f", configFile).Run(); err != nil {
		return err
	}
	log.Debug("Removed ", configFile)
	return nil
}

func removeConfigFile(root, id string) error {
	configFile := getConfigFilename(root, id)
	if err := exec.Command("rm", "-f", configFile).Run(); err != nil {
		return err
	}
	log.Debug("Removed ", configFile)
	return nil
}

func Deregister(root, kind, id string) error {
	err := removeDriverConfigFile(root, kind, id)
	if err != nil {
		return err
	}
	err = removeConfigFile(root, id)
	if err != nil {
		return err
	}
	return nil
}

func AddVolume(root, id, volumeId, base string, size uint64) error {
	configFile := getConfigFilename(root, id)
	b := &BlockStore{}
	err := utils.LoadConfig(configFile, b)
	if err != nil {
		return err
	}

	if _, exists := b.Volumes[volumeId]; exists {
		return fmt.Errorf("volume %v already exists in blockstore %v", volumeId, id)
	}

	driverConfigFile := getDriverConfigFilename(root, b.Kind, id)
	driver, err := GetBlockStoreDriver(b.Kind, driverConfigFile, id, nil)
	if err != nil {
		return err
	}

	volumeDir := filepath.Join(BLOCKSTORE_BASE, VOLUME_DIRECTORY, volumeId)
	err = driver.MkDirAll(volumeDir)
	if err != nil {
		return err
	}
	log.Debug("Created volume directory: ", volumeDir)
	volume := Volume{
		Size:           size,
		Base:           base,
		LastSnapshotId: "",
	}
	b.Volumes[volumeId] = volume
	if err = utils.SaveConfig(configFile, b); err != nil {
		return err
	}

	j, err := json.Marshal(volume)
	if err != nil {
		return err
	}
	volumePath := getVolumePath(volumeId)
	volumeFile := VOLUME_CONFIG_FILE
	if driver.FileExists(volumePath, volumeFile) {
		return fmt.Errorf("volume config file already existed in blockstore")
	}
	if err := driver.Write(j, volumePath, volumeFile); err != nil {
		return err
	}
	log.Debug("Created volume configuration file done: ", filepath.Join(volumePath, volumeFile))

	return nil
}

func RemoveVolume(root, id, volumeId string) error {
	configFile := getConfigFilename(root, id)
	b := &BlockStore{}
	err := utils.LoadConfig(configFile, b)
	if err != nil {
		return err
	}
	if _, exists := b.Volumes[volumeId]; !exists {
		return fmt.Errorf("volume %v doesn't exist in blockstore %v", volumeId, id)
	}

	driverConfigFile := getDriverConfigFilename(root, b.Kind, id)
	driver, err := GetBlockStoreDriver(b.Kind, driverConfigFile, id, nil)
	if err != nil {
		return err
	}

	volumeDir := filepath.Join(BLOCKSTORE_BASE, VOLUME_DIRECTORY, volumeId)
	err = driver.RemoveAll(volumeDir)
	if err != nil {
		return err
	}
	log.Debug("Removed volume directory: ", volumeDir)
	delete(b.Volumes, volumeId)

	if err = utils.SaveConfig(configFile, b); err != nil {
		return err
	}
	return nil
}

func getVolumePath(volumeId string) string {
	return filepath.Join(BLOCKSTORE_BASE, VOLUME_DIRECTORY, volumeId)
}

func getSnapshotsPath(volumeId string) string {
	return filepath.Join(getVolumePath(volumeId), SNAPSHOTS_DIRECTORY)
}

func getBlockPathAndFileName(volumeId, checksum string) (string, string) {
	blockSubDirLayer1 := checksum[0:BLOCK_SEPARATE_LAYER1]
	blockSubDirLayer2 := checksum[BLOCK_SEPARATE_LAYER1:BLOCK_SEPARATE_LAYER2]
	path := filepath.Join(getVolumePath(volumeId), BLOCKS_DIRECTORY, blockSubDirLayer1, blockSubDirLayer2)
	fileName := checksum + ".blk"

	return path, fileName
}

func getSnapshotConfigName(id string) string {
	return SNAPSHOT_CONFIG_PREFIX + id + ".cfg"
}

func BackupSnapshot(root, snapshotId, volumeId, blockstoreId string, sDriver drivers.Driver) error {
	configFile := getConfigFilename(root, blockstoreId)
	b := &BlockStore{}
	err := utils.LoadConfig(configFile, b)
	if err != nil {
		return err
	}
	driverConfigFile := getDriverConfigFilename(root, b.Kind, blockstoreId)
	bsDriver, err := GetBlockStoreDriver(b.Kind, driverConfigFile, blockstoreId, nil)
	if err != nil {
		return err
	}

	volume, exists := b.Volumes[volumeId]
	if !exists {
		return fmt.Errorf("cannot find volume %v in blockstore %v", volumeId, blockstoreId)
	}

	lastSnapshotId := volume.LastSnapshotId
	lastSnapshotMap := &SnapshotMap{}
	//We'd better check last snapshot config early, ensure it would go through
	if lastSnapshotId != "" {
		path := getSnapshotsPath(volumeId)
		fileName := getSnapshotConfigName(lastSnapshotId)
		fileSize := bsDriver.FileSize(path, fileName)
		if fileSize < 0 {
			return fmt.Errorf("Last snapshot %v doesn't existed in blockstore", lastSnapshotId)
		}
		data := make([]byte, fileSize)
		if err := bsDriver.Read(path, fileName, data); err != nil {
			return err
		}
		err := json.Unmarshal(data, lastSnapshotMap)
		if err != nil {
			return err
		}
		log.Debug("Loaded last snapshot %v", lastSnapshotId)
	}

	delta := metadata.Mappings{}
	if err = sDriver.CompareSnapshot(snapshotId, lastSnapshotId, volumeId, &delta); err != nil {
		return err
	}
	if delta.BlockSize != b.BlockSize {
		return fmt.Errorf("Currently doesn't support different block sizes between blockstore and driver")
	}

	snapshotDeltaMap := &SnapshotMap{
		Blocks: []BlockMapping{},
	}
	for _, d := range delta.Mappings {
		block := make([]byte, b.BlockSize)
		for i := uint64(0); i < d.Size/uint64(delta.BlockSize); i++ {
			offset := d.Offset + i*uint64(delta.BlockSize)
			err := sDriver.ReadSnapshot(snapshotId, volumeId, offset, block)
			if err != nil {
				return err
			}
			checksumBytes := sha512.Sum512(block)
			checksum := hex.EncodeToString(checksumBytes[:])[:PRESERVED_CHECKSUM_LENGTH]
			path, fileName := getBlockPathAndFileName(volumeId, checksum)
			if bsDriver.FileSize(path, fileName) >= 0 {
				log.Debugln("Found existed block match at ", path, fileName)
				continue
			}
			log.Debugln("Creating new block file at ", path, fileName)
			if err := bsDriver.MkDirAll(path); err != nil {
				return err
			}
			if err := bsDriver.Write(block, path, fileName); err != nil {
				return err
			}
			log.Debugln("Created new block file at ", path, fileName)

			blockMapping := BlockMapping{
				Offset: offset,
				Block:  checksum,
			}
			snapshotDeltaMap.Blocks = append(snapshotDeltaMap.Blocks, blockMapping)
		}
	}

	snapshotMap := mergeSnapshotMap(snapshotId, snapshotDeltaMap, lastSnapshotMap)
	path := getSnapshotsPath(volumeId)
	fileName := getSnapshotConfigName(snapshotId)
	if bsDriver.FileExists(path, fileName) {
		file := filepath.Join(path, fileName)
		log.Errorf("Snapshot configuration file %v already exists, would remove it\n", file)
		if err := bsDriver.RemoveAll(file); err != nil {
			return err
		}
	}
	j, err := json.Marshal(*snapshotMap)
	if err != nil {
		return err
	}
	if err := bsDriver.Write(j, path, fileName); err != nil {
		return err
	}

	volume.LastSnapshotId = snapshotId
	if err := utils.SaveConfig(configFile, b); err != nil {
		return err
	}

	return nil
}

func mergeSnapshotMap(snapshotId string, deltaMap, lastMap *SnapshotMap) *SnapshotMap {
	if len(lastMap.Blocks) == 0 {
		deltaMap.Id = snapshotId
		return deltaMap
	}
	sMap := &SnapshotMap{
		Id:     snapshotId,
		Blocks: []BlockMapping{},
	}
	for d, l := 0, 0; d < len(deltaMap.Blocks) && l < len(lastMap.Blocks); {
		dB := deltaMap.Blocks[d]
		lB := lastMap.Blocks[l]
		if dB.Offset == lB.Offset {
			sMap.Blocks = append(sMap.Blocks, dB)
			d++
			l++
		} else if dB.Offset < lB.Offset {
			sMap.Blocks = append(sMap.Blocks, dB)
			d++
		} else {
			//dB.Offset > lB.offset
			sMap.Blocks = append(sMap.Blocks, lB)
			l++
		}
	}

	return sMap
}
