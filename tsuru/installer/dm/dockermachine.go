// Copyright 2016 tsuru-client authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dm

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/docker/machine/libmachine/cert"
	"github.com/docker/machine/libmachine/mcnutils"
	"github.com/tsuru/tsuru/cmd"
	"github.com/tsuru/tsuru/iaas/dockermachine"
)

var (
	storeBasePath              = cmd.JoinWithUserDir(".tsuru", "installs")
	DefaultDockerMachineConfig = &DockerMachineConfig{
		DriverName: "virtualbox",
		Name:       "tsuru",
		DriverOpts: make(map[string]interface{}),
	}
)

type DockerMachine struct {
	driverName       string
	Name             string
	storePath        string
	certsPath        string
	API              dockermachine.DockerMachineAPI
	machinesCount    uint64
	globalDriverOpts map[string]interface{}
	dockerHubMirror  string
}

type DockerMachineConfig struct {
	DriverName      string
	CAPath          string
	Name            string
	DriverOpts      map[string]interface{}
	DockerHubMirror string
}

type MachineProvisioner interface {
	ProvisionMachine(map[string]interface{}) (*dockermachine.Machine, error)
}

func NewDockerMachine(config *DockerMachineConfig) (*DockerMachine, error) {
	storePath := filepath.Join(storeBasePath, config.Name)
	certsPath := filepath.Join(storePath, "certs")
	dm, err := dockermachine.NewDockerMachine(dockermachine.DockerMachineConfig{
		CaPath:    config.CAPath,
		OutWriter: os.Stdout,
		ErrWriter: os.Stderr,
		StorePath: storePath,
	})
	if err != nil {
		return nil, err
	}
	return &DockerMachine{
		driverName:       config.DriverName,
		Name:             config.Name,
		API:              dm,
		globalDriverOpts: config.DriverOpts,
		dockerHubMirror:  config.DockerHubMirror,
		certsPath:        certsPath,
		storePath:        storePath,
	}, nil
}

func (d *DockerMachine) ProvisionMachine(driverOpts map[string]interface{}) (*dockermachine.Machine, error) {
	m, err := d.CreateMachine(driverOpts)
	if err != nil {
		return nil, fmt.Errorf("error creating machine %s", err)
	}
	err = d.uploadRegistryCertificate(GetPrivateIP(m), m.Host.Driver.GetSSHUsername(), m.Host)
	if err != nil {
		return nil, fmt.Errorf("error uploading registry certificates to %s: %s", m.Base.Address, err)
	}
	return m, nil
}

func (d *DockerMachine) CreateMachine(driverOpts map[string]interface{}) (*dockermachine.Machine, error) {
	driverOpts["swarm-master"] = false
	driverOpts["swarm-host"] = ""
	driverOpts["engine-install-url"] = ""
	driverOpts["swarm-discovery"] = ""
	mergedOpts := make(map[string]interface{})
	for k, v := range d.globalDriverOpts {
		mergedOpts[k] = v
	}
	for k, v := range driverOpts {
		mergedOpts[k] = v
	}
	m, err := d.API.CreateMachine(dockermachine.CreateMachineOpts{
		Name:           d.generateMachineName(),
		DriverName:     d.driverName,
		Params:         mergedOpts,
		RegistryMirror: d.dockerHubMirror,
	})
	if err != nil {
		return nil, err
	}
	if m.Host.AuthOptions() != nil {
		m.Host.AuthOptions().ServerCertSANs = append(m.Host.AuthOptions().ServerCertSANs, GetPrivateIP(m))
		err = m.Host.ConfigureAuth()
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (d *DockerMachine) generateMachineName() string {
	atomic.AddUint64(&d.machinesCount, 1)
	return fmt.Sprintf("%s-%d", d.Name, atomic.LoadUint64(&d.machinesCount))
}

func (d *DockerMachine) uploadRegistryCertificate(ip, user string, target sshTarget) error {
	registryCertPath := filepath.Join(d.certsPath, "registry-cert.pem")
	registryKeyPath := filepath.Join(d.certsPath, "registry-key.pem")
	var registryIP string
	if _, errReg := os.Stat(registryCertPath); os.IsNotExist(errReg) {
		errCreate := d.createRegistryCertificate(ip)
		if errCreate != nil {
			return errCreate
		}
		registryIP = ip
	} else {
		certData, errRead := ioutil.ReadFile(registryCertPath)
		if errRead != nil {
			return fmt.Errorf("failed to read registry-cert.pem: %s", errRead)
		}
		block, _ := pem.Decode(certData)
		cert, errRead := x509.ParseCertificate(block.Bytes)
		if errRead != nil {
			return fmt.Errorf("failed to parse registry certificate: %s", errRead)
		}
		registryIP = cert.IPAddresses[0].String()
	}
	fmt.Printf("Uploading registry certificate...\n")
	certsBasePath := fmt.Sprintf("/home/%s/certs/%s:5000", user, registryIP)
	if _, err := target.RunSSHCommand(fmt.Sprintf("mkdir -p %s", certsBasePath)); err != nil {
		return err
	}
	dockerCertsPath := "/etc/docker/certs.d"
	if _, err := target.RunSSHCommand(fmt.Sprintf("sudo mkdir %s", dockerCertsPath)); err != nil {
		return err
	}
	fileCopies := map[string]string{
		registryCertPath:                         filepath.Join(certsBasePath, "registry-cert.pem"),
		registryKeyPath:                          filepath.Join(certsBasePath, "registry-key.pem"),
		filepath.Join(d.certsPath, "ca-key.pem"): filepath.Join(dockerCertsPath, "ca-key.pem"),
		filepath.Join(d.certsPath, "ca.pem"):     filepath.Join(dockerCertsPath, "ca.pem"),
		filepath.Join(d.certsPath, "cert.pem"):   filepath.Join(dockerCertsPath, "cert.pem"),
		filepath.Join(d.certsPath, "key.pem"):    filepath.Join(dockerCertsPath, "key.pem"),
	}
	for src, dst := range fileCopies {
		errWrite := writeRemoteFile(target, src, dst)
		if errWrite != nil {
			return errWrite
		}
	}
	if _, err := target.RunSSHCommand(fmt.Sprintf("sudo cp -r /home/%s/certs/* %s/", user, dockerCertsPath)); err != nil {
		return err
	}
	if _, err := target.RunSSHCommand(fmt.Sprintf("sudo cat %s/ca.pem | sudo tee -a /etc/ssl/certs/ca-certificates.crt", dockerCertsPath)); err != nil {
		return err
	}
	_, err := target.RunSSHCommand("sudo mkdir -p /var/lib/registry/")
	return err
}

func (d *DockerMachine) createRegistryCertificate(hosts ...string) error {
	fmt.Printf("Creating registry certificate...\n")
	caOrg := mcnutils.GetUsername()
	org := caOrg + ".<bootstrap>"
	generator := &cert.X509CertGenerator{}
	certOpts := &cert.Options{
		Hosts:       hosts,
		CertFile:    filepath.Join(d.certsPath, "registry-cert.pem"),
		KeyFile:     filepath.Join(d.certsPath, "registry-key.pem"),
		CAFile:      filepath.Join(d.certsPath, "ca.pem"),
		CAKeyFile:   filepath.Join(d.certsPath, "ca-key.pem"),
		Org:         org,
		Bits:        2048,
		SwarmMaster: false,
	}
	return generator.GenerateCert(certOpts)
}

func (d *DockerMachine) DeleteAll() error {
	return d.API.DeleteAll()
}

func (d *DockerMachine) Close() error {
	return d.API.Close()
}
