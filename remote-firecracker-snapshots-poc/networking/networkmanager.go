// Package networking provides primitives to connect function instances to the network.
package networking

import (
	"log"
	"sync"

	"github.com/pkg/errors"
)

// NetworkManager manages the in use network configurations along with a pool of free network configurations
// that can be used to connect a function instance to the network.
type NetworkManager struct {
	sync.Mutex
	nextID        int
	hostIfaceName string

	// Pool of free network configs
	networkPool []*NetworkConfig
	poolCond    *sync.Cond
	poolSize    int

	// Mapping of function instance IDs to their network config
	netConfigs map[string]*NetworkConfig

	// Network configs that are being created
	inCreation sync.WaitGroup
}

// NewNetworkManager creates and returns a new network manager that connects function instances to the network
// using the supplied interface. If no interface is supplied, the default interface is used. To take the network
// setup of the critical path of a function creation, the network manager tries to maintain a pool of ready to use
// network configurations of size at least poolSize.
func NewNetworkManager(hostIfaceName string, poolSize int) (*NetworkManager, error) {
	manager := new(NetworkManager)

	manager.hostIfaceName = hostIfaceName
	if manager.hostIfaceName == "" {
		hostIface, err := getHostIfaceName()
		if err != nil {
			return nil, err
		} else {
			manager.hostIfaceName = hostIface
		}
	}

	manager.netConfigs = make(map[string]*NetworkConfig)
	manager.networkPool = make([]*NetworkConfig, 0)

	startId, err := getNetworkStartID()
	if err == nil {
		manager.nextID = startId
	} else {
		manager.nextID = 0
	}

	manager.poolCond = sync.NewCond(new(sync.Mutex))
	manager.initConfigPool(poolSize)
	manager.poolSize = poolSize

	return manager, nil
}

// initConfigPool fills an empty network pool up to the given poolSize
func (mgr *NetworkManager) initConfigPool(poolSize int) {
	var wg sync.WaitGroup
	wg.Add(poolSize)

	log.Printf("Initializing network pool with %d network configs", poolSize)

	// Concurrently create poolSize network configs
	for i := 0; i < poolSize; i++ {
		go func() {
			mgr.addNetConfig()
			wg.Done()
		}()
	}
	wg.Wait()
}

// addNetConfig creates and initializes a new network config
func (mgr *NetworkManager) addNetConfig() {
	mgr.Lock()
	id := mgr.nextID
	mgr.nextID += 1
	mgr.inCreation.Add(1)
	mgr.Unlock()

	netCfg := NewNetworkConfig(id, mgr.hostIfaceName)
	if err := netCfg.CreateNetwork(); err != nil {
		errors.Wrapf(err, "failed to create network")
	}

	mgr.poolCond.L.Lock()
	mgr.networkPool = append(mgr.networkPool, netCfg)
	// Signal in case someone is waiting for a new config to become available in the pool
	mgr.poolCond.Signal()
	mgr.poolCond.L.Unlock()
	mgr.inCreation.Done()
}

// allocNetConfig allocates a new network config from the pool to a function instance identified by funcID
func (mgr *NetworkManager) allocNetConfig(funcID string) *NetworkConfig {
	// Add netconfig to pool to keep pool to configured size
	go mgr.addNetConfig()
	log.Printf("Allocating a new network config from network pool to function instance")

	// Pop a network config from the pool and allocate it to the function instance
	mgr.poolCond.L.Lock()
	for len(mgr.networkPool) == 0 {
		// Wait until a new network config has been created
		mgr.poolCond.Wait()
	}

	config := mgr.networkPool[len(mgr.networkPool)-1]
	mgr.networkPool = mgr.networkPool[:len(mgr.networkPool)-1]
	mgr.poolCond.L.Unlock()

	mgr.Lock()
	mgr.netConfigs[funcID] = config
	mgr.Unlock()

	log.Printf("Network config: funcID: %s, ContainerIP: %s, NamespaceName: %s, Veth0CIDR: %s, Veth0Name: %s, Veth1CIDR: %s, Veth1Name: %s, CloneIP: %s, ContainerCIDR: %s, GatewayIP: %s, HostDevName: %s, NamespacePath: %s", funcID, config.getContainerIP(), config.getNamespaceName(), config.getVeth0CIDR(), config.getVeth0Name(), config.getVeth1CIDR(), config.getVeth1Name(), config.GetCloneIP(), config.GetContainerCIDR(), config.GetGatewayIP(), config.GetHostDevName(), config.GetNamespacePath())

	return config
}

// releaseNetConfig releases the network config of a given function instance with id funcID back to the pool
func (mgr *NetworkManager) releaseNetConfig(funcID string) {
	mgr.Lock()
	config := mgr.netConfigs[funcID]
	delete(mgr.netConfigs, funcID)
	mgr.Unlock()

	log.Printf("Releasing network config from function instance and adding it to network pool")

	// Add network config back to the pool. We allow the pool to grow over it's configured size here since the
	// overhead of keeping a network config in the pool is low compared to the cost of creating a new config.
	mgr.poolCond.L.Lock()
	mgr.networkPool = append(mgr.networkPool, config)
	mgr.poolCond.Signal()
	mgr.poolCond.L.Unlock()
}

// CreateNetwork creates the networking for a function instance identified by funcID
func (mgr *NetworkManager) CreateNetwork(funcID string) (*NetworkConfig, error) {
	log.Printf("Creating network config for function instance %s", funcID)

	netCfg := mgr.allocNetConfig(funcID)
	return netCfg, nil
}

// GetConfig returns the network config assigned to a function instance identified by funcID
func (mgr *NetworkManager) GetConfig(funcID string) *NetworkConfig {
	mgr.Lock()
	defer mgr.Unlock()

	cfg := mgr.netConfigs[funcID]
	return cfg
}

// RemoveNetwork removes the network config of a function instance identified by funcID. The allocated network devices
// for the given function instance must not be in use anymore when calling this function.
func (mgr *NetworkManager) RemoveNetwork(funcID string) error {
	log.Printf("Removing network config for function instance %s", funcID)
	mgr.releaseNetConfig(funcID)
	return nil
}

// Cleanup removes and deallocates all network configurations that are in use or in the network pool. Make sure to first
// clean up all running functions before removing their network configs.
func (mgr *NetworkManager) Cleanup() error {
	log.Println("Cleaning up network manager")
	mgr.Lock()
	defer mgr.Unlock()

	// Wait till all network configs still in creation are added
	mgr.inCreation.Wait()

	// Release network configs still in use
	var wgu sync.WaitGroup
	wgu.Add(len(mgr.netConfigs))
	for funcID := range mgr.netConfigs {
		config := mgr.netConfigs[funcID]
		go func(config *NetworkConfig) {
			if err := config.RemoveNetwork(); err != nil {
				errors.Wrapf(err, "failed to remove network")
			}
			wgu.Done()
		}(config)
	}
	wgu.Wait()
	mgr.netConfigs = make(map[string]*NetworkConfig)

	// Cleanup network pool
	mgr.poolCond.L.Lock()
	var wg sync.WaitGroup
	wg.Add(len(mgr.networkPool))

	for _, config := range mgr.networkPool {
		go func(config *NetworkConfig) {
			if err := config.RemoveNetwork(); err != nil {
				errors.Wrapf(err, "failed to remove network")
			}
			wg.Done()
		}(config)
	}
	wg.Wait()
	mgr.networkPool = make([]*NetworkConfig, 0)
	mgr.poolCond.L.Unlock()

	return nil
}