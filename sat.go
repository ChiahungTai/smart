// Copyright 2017 Daniel Swarbrick. All rights reserved.
// Use of this source code is governed by a GPL license that can be found in the LICENSE file.

// SCSI / ATA Translation functions.

package smart

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"path/filepath"
)

const (
	// ATA feature register values for SMART
	SMART_READ_DATA     = 0xd0
	SMART_READ_LOG      = 0xd5
	SMART_RETURN_STATUS = 0xda

	// ATA commands
	ATA_SMART           = 0xb0
	ATA_IDENTIFY_DEVICE = 0xec
)

// SATDevice is a simple wrapper around an embedded SCSIDevice type, which handles sending ATA
// commands via SCSI pass-through (SCSI-ATA Translation).
type SATDevice struct {
	SCSIDevice
}

func (d *SATDevice) identify() (IdentifyDeviceData, error) {
	var identBuf IdentifyDeviceData

	respBuf := make([]byte, 512)

	cdb16 := CDB16{SCSI_ATA_PASSTHRU_16}
	cdb16[1] = 0x08                 // ATA protocol (4 << 1, PIO data-in)
	cdb16[2] = 0x0e                 // BYT_BLOK = 1, T_LENGTH = 2, T_DIR = 1
	cdb16[14] = ATA_IDENTIFY_DEVICE // command

	if err := d.sendCDB(cdb16[:], &respBuf); err != nil {
		return identBuf, fmt.Errorf("sendCDB ATA IDENTIFY: %v", err)
	}

	binary.Read(bytes.NewBuffer(respBuf), nativeEndian, &identBuf)
	swapBytes(identBuf.SerialNumber[:])
	swapBytes(identBuf.FirmwareRevision[:])
	swapBytes(identBuf.ModelNumber[:])

	return identBuf, nil
}

// Read SMART log page (WIP / experimental)
func (d *SATDevice) readSMARTLog(logPage uint8) ([]byte, error) {
	respBuf := make([]byte, 512)

	cdb := CDB16{SCSI_ATA_PASSTHRU_16}
	cdb[1] = 0x08           // ATA protocol (4 << 1, PIO data-in)
	cdb[2] = 0x0e           // BYT_BLOK = 1, T_LENGTH = 2, T_DIR = 1
	cdb[4] = SMART_READ_LOG // feature LSB
	cdb[6] = 0x01           // sector count
	cdb[8] = logPage        // SMART log page number
	cdb[10] = 0x4f          // low lba_mid
	cdb[12] = 0xc2          // low lba_high
	cdb[14] = ATA_SMART     // command

	if err := d.sendCDB(cdb[:], &respBuf); err != nil {
		return respBuf, fmt.Errorf("sendCDB SMART READ LOG: %v", err)
	}

	return respBuf, nil
}

func (d *SATDevice) PrintSMART(db *driveDb) error {
	// Standard SCSI INQUIRY command
	inqResp, err := d.inquiry()
	if err != nil {
		return fmt.Errorf("SgExecute INQUIRY: %v", err)
	}

	fmt.Println("SCSI INQUIRY:", inqResp)

	identBuf, err := d.identify()
	if err != nil {
		return err
	}

	fmt.Println("\nATA IDENTIFY data follows:")
	fmt.Printf("Serial Number: %s\n", identBuf.SerialNumber)
	fmt.Println("LU WWN Device Id:", identBuf.getWWN())
	fmt.Printf("Firmware Revision: %s\n", identBuf.FirmwareRevision)
	fmt.Printf("Model Number: %s\n", identBuf.ModelNumber)
	fmt.Printf("Rotation Rate: %d\n", identBuf.RotationRate)
	fmt.Printf("SMART support available: %v\n", identBuf.Word87>>14 == 1)
	fmt.Printf("SMART support enabled: %v\n", identBuf.Word85&0x1 != 0)
	fmt.Println("ATA Major Version:", identBuf.getATAMajorVersion())
	fmt.Println("ATA Minor Version:", identBuf.getATAMinorVersion())
	fmt.Println("Transport:", identBuf.getTransport())

	thisDrive := db.lookupDrive(identBuf.ModelNumber[:])
	fmt.Printf("Drive DB contains %d entries. Using model: %s\n", len(db.Drives), thisDrive.Family)

	// FIXME: Check that device supports SMART before trying to read data page

	/*
	 * SMART READ DATA
	 */
	cdb := CDB16{SCSI_ATA_PASSTHRU_16}
	cdb[1] = 0x08            // ATA protocol (4 << 1, PIO data-in)
	cdb[2] = 0x0e            // BYT_BLOK = 1, T_LENGTH = 2, T_DIR = 1
	cdb[4] = SMART_READ_DATA // feature LSB
	cdb[10] = 0x4f           // low lba_mid
	cdb[12] = 0xc2           // low lba_high
	cdb[14] = ATA_SMART      // command

	respBuf := make([]byte, 512)

	if err := d.sendCDB(cdb[:], &respBuf); err != nil {
		return fmt.Errorf("sendCDB SMART READ DATA: %v", err)
	}

	smart := smartPage{}
	binary.Read(bytes.NewBuffer(respBuf[:362]), nativeEndian, &smart)
	printSMARTPage(smart, thisDrive)

	// Read SMART log directory
	logBuf, err := d.readSMARTLog(0x00)
	if err != nil {
		return err
	}

	smartLogDir := smartLogDirectory{}
	binary.Read(bytes.NewBuffer(logBuf), nativeEndian, &smartLogDir)
	fmt.Printf("\nSMART log directory: %+v\n", smartLogDir)

	// Read SMART error log
	logBuf, err = d.readSMARTLog(0x01)
	if err != nil {
		return err
	}

	sumErrLog := smartSummaryErrorLog{}
	binary.Read(bytes.NewBuffer(logBuf), nativeEndian, &sumErrLog)
	fmt.Printf("\nSummary SMART error log: %+v\n", sumErrLog)

	// Read SMART self-test log
	logBuf, err = d.readSMARTLog(0x06)
	if err != nil {
		return err
	}

	selfTestLog := smartSelfTestLog{}
	binary.Read(bytes.NewBuffer(logBuf), nativeEndian, &selfTestLog)
	fmt.Printf("\nSMART self-test log: %+v\n", selfTestLog)

	return nil
}

// TODO: Make this discover NVMe and MegaRAID devices also.
func ScanDevices() []SCSIDevice {
	var devices []SCSIDevice

	// Find all SCSI disk devices
	files, err := filepath.Glob("/dev/sd*[^0-9]")
	if err != nil {
		return devices
	}

	for _, file := range files {
		devices = append(devices, SCSIDevice{Name: file, fd: -1})
	}

	return devices
}
