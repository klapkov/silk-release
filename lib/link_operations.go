package lib

import (
	"fmt"
	"net"

	"github.com/containernetworking/cni/pkg/types"
	"github.com/vishvananda/netlink"
)

// LinkOperations exposes mid-level link setup operations.
// They encapsulate low-level netlink and sysctl commands.
type LinkOperations struct {
	SysctlAdapter  sysctlAdapter
	NetlinkAdapter netlinkAdapter
}

func (s *LinkOperations) DisableIPv6(deviceName string) error {
	_, err := s.SysctlAdapter.Sysctl(fmt.Sprintf("net.ipv6.conf.%s.disable_ipv6", deviceName), "1")
	if err != nil {
		return fmt.Errorf("disabling IPv6: %s", err)
	}
	return nil
}

func (s *LinkOperations) EnableIPv4Forwarding() error {
	_, err := s.SysctlAdapter.Sysctl("net.ipv4.ip_forward", "1")
	if err != nil {
		return fmt.Errorf("enabling IPv4 forwarding: %s", err)
	}
	return nil
}

// StaticNeighborNoARP disables ARP on the link and installs a single permanent neighbor rule
// that resolves the given destIP to the given hardware address
func (s *LinkOperations) StaticNeighborNoARP(link netlink.Link, destIP net.IP, hwAddr net.HardwareAddr) error {
	err := s.NetlinkAdapter.LinkSetARPOff(link)
	if err != nil {
		return fmt.Errorf("set ARP off: %s", err)
	}

	err = s.NetlinkAdapter.NeighAddPermanentIPv4(link.Attrs().Index, destIP, hwAddr)
	if err != nil {
		return fmt.Errorf("neigh add: %s", err)
	}

	return nil
}

func (s *LinkOperations) SetPointToPointAddress(link netlink.Link, localIPAddr, peerIPAddr net.IP) error {
	localAddr := &net.IPNet{
		IP:   localIPAddr,
		Mask: []byte{255, 255, 255, 255},
	}
	peerAddr := &net.IPNet{
		IP:   peerIPAddr,
		Mask: []byte{255, 255, 255, 255},
	}
	addr, err := s.NetlinkAdapter.ParseAddr(localAddr.String())
	if err != nil {
		return fmt.Errorf("parsing address %s: %s", localAddr, err)
	}

	addr.Peer = peerAddr

	err = s.NetlinkAdapter.AddrAddScopeLink(link, addr)
	if err != nil {
		return fmt.Errorf("adding IP address %s: %s", localAddr, err)
	}

	return nil
}

func (s *LinkOperations) RenameLink(oldName, newName string) error {
	link, err := s.NetlinkAdapter.LinkByName(oldName)
	if err != nil {
		return fmt.Errorf("failed to find link %q: %s", oldName, err)
	}

	err = s.NetlinkAdapter.LinkSetName(link, newName)
	if err != nil {
		return fmt.Errorf("rename link: %s", err)
	}

	return nil
}

func (s *LinkOperations) DeleteLinkByName(deviceName string) error {
	link, err := s.NetlinkAdapter.LinkByName(deviceName)
	if err != nil {
		return fmt.Errorf("failed to find link %q: %s", deviceName, err)
	}

	return s.NetlinkAdapter.LinkDel(link)
}

func (s *LinkOperations) RouteAdd(route netlink.Route) error {
	err := s.NetlinkAdapter.RouteAdd(route)
	if err != nil {
		return fmt.Errorf("failed to add route %s: %s", route, err)
	}
	return nil
}

func (s *LinkOperations) RouteAddAll(routes []*types.Route, sourceIP net.IP) error {
	for _, r := range routes {
		dst := r.Dst
		err := s.NetlinkAdapter.RouteAdd(netlink.Route{
			Src: sourceIP,
			Dst: &dst,
			Gw:  r.GW,
		})
		if err != nil {
			return fmt.Errorf("adding route in container: %s", err)
		}
	}
	return nil
}
