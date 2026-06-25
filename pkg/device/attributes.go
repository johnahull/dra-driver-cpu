/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package device

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

// SetCompatibilityAttributes add attributes to enable compatibility (e.g. alignment) with other
// DRA resource drivers leveraging attributes which are not kubernetes standard.
// This is the "staging area" which enables attribute sharing until (or before) they become standard.
func SetCompatibilityAttributes(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, numaID int64, numaListEnabled bool) {
	attrs["dra.net/numaNode"] = resourceapi.DeviceAttribute{IntValue: ptr.To(numaID)}
	numaAttr, err := deviceattribute.GetNUMANodeAttribute(int(numaID), numaListEnabled)
	if err == nil {
		attrs[numaAttr.Name] = numaAttr.Value
	}

	pcieRoots, err := GetPCIeRootsForNUMANode(int(numaID))
	if err != nil {
		klog.V(4).Infof("Failed to get PCIe roots for NUMA %d: %v", numaID, err)
	} else if len(pcieRoots) > 0 {
		attrs["resource.kubernetes.io/pcieRoot"] = resourceapi.DeviceAttribute{StringValues: pcieRoots}
	}
}

// GetPCIeRootsForNUMANode returns the PCIe root complexes adjacent to CPUs
// on the given NUMA node. It scans PCI bridge devices (class 0x06**) in
// sysfs, reads their local_cpulist, and collects roots for CPUs on the
// target NUMA node. This enables KEP-5491 list-type matchAttribute
// constraints across PCI and non-PCI devices.
func GetPCIeRootsForNUMANode(numaNode int) ([]string, error) {
	cpulistBytes, err := os.ReadFile(fmt.Sprintf("/sys/devices/system/node/node%d/cpulist", numaNode))
	if err != nil {
		return nil, fmt.Errorf("failed to read cpulist for NUMA node %d: %w", numaNode, err)
	}
	numaCPUs := parseCPUList(strings.TrimSpace(string(cpulistBytes)))
	numaCPUSet := make(map[int]bool, len(numaCPUs))
	for _, c := range numaCPUs {
		numaCPUSet[c] = true
	}

	rootSet := make(map[string]bool)

	entries, err := os.ReadDir("/sys/bus/pci/devices")
	if err != nil {
		return nil, fmt.Errorf("failed to read PCI devices: %w", err)
	}

	for _, entry := range entries {
		devPath := filepath.Join("/sys/bus/pci/devices", entry.Name())

		classBytes, err := os.ReadFile(filepath.Join(devPath, "class"))
		if err != nil {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(string(classBytes)), "0x06") {
			continue
		}

		cpulistBytes, err := os.ReadFile(filepath.Join(devPath, "local_cpulist"))
		if err != nil {
			continue
		}
		bridgeCPUs := parseCPUList(strings.TrimSpace(string(cpulistBytes)))

		hasLocalCPU := false
		for _, c := range bridgeCPUs {
			if numaCPUSet[c] {
				hasLocalCPU = true
				break
			}
		}
		if !hasLocalCPU {
			continue
		}

		target, err := os.Readlink(filepath.Join("/sys/bus/pci/devices", entry.Name()))
		if err != nil {
			continue
		}
		parts := strings.Split(target, "/")
		for _, p := range parts {
			if strings.HasPrefix(p, "pci") {
				rootSet[p] = true
				break
			}
		}
	}

	roots := make([]string, 0, len(rootSet))
	for r := range rootSet {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	return roots, nil
}

func parseCPUList(cpulist string) []int {
	var cpus []int
	for _, part := range strings.Split(cpulist, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.Index(part, "-"); idx >= 0 {
			lo, err1 := strconv.Atoi(part[:idx])
			hi, err2 := strconv.Atoi(part[idx+1:])
			if err1 != nil || err2 != nil {
				continue
			}
			for i := lo; i <= hi; i++ {
				cpus = append(cpus, i)
			}
		} else {
			v, err := strconv.Atoi(part)
			if err != nil {
				continue
			}
			cpus = append(cpus, v)
		}
	}
	return cpus
}
