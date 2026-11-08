import React, { useEffect, useRef, useState } from 'react';
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';
import { faEthernet, faServer } from '@fortawesome/free-solid-svg-icons';
import { Link } from 'react-router-dom';
import { Server } from '@/api/server/getServer';
import getServerResourceUsage, { ServerStats } from '@/api/server/getServerResourceUsage';
import { bytesToString, ip, mbToBytes } from '@/lib/formatters';
import tw from 'twin.macro';
import Spinner from '@/components/elements/Spinner';
import StatusChip, { Tone } from '@/components/elements/StatusChip';

// How much of the limit is used, from 0 to 1, or null when there is no limit to measure against.
const share = (used: number, limit: number): number | null => (limit > 0 ? Math.min(used / limit, 1) : null);

const Meter = ({ label, value, of, ratio }: { label: string; value: string; of: string; ratio: number | null }) => (
    <div css={tw`min-w-0`}>
        <div css={tw`flex items-baseline justify-between text-xs mb-1`}>
            <span css={tw`text-neutral-400`}>{label}</span>
            <span css={tw`font-mono text-neutral-100 truncate ml-2`}>{value}</span>
        </div>
        <div css={tw`h-1 rounded-full bg-neutral-700 overflow-hidden`}>
            {ratio !== null && (
                <div
                    css={[tw`h-full rounded-full`, ratio >= 0.9 ? tw`bg-red-400` : tw`bg-ember-500`]}
                    style={{ width: `${Math.round(ratio * 100)}%` }}
                />
            )}
        </div>
        <p css={tw`mt-1 text-2xs text-neutral-500 truncate`}>of {of}</p>
    </div>
);

type Timer = ReturnType<typeof setInterval>;

export default ({ server, className }: { server: Server; className?: string }) => {
    const interval = useRef<Timer>(null) as React.MutableRefObject<Timer>;
    const [isSuspended, setIsSuspended] = useState(server.status === 'suspended');
    const [stats, setStats] = useState<ServerStats | null>(null);
    const [unreachable, setUnreachable] = useState(false);

    const getStats = () =>
        getServerResourceUsage(server.uuid)
            .then((data) => {
                setStats(data);
                setUnreachable(false);
            })
            .catch((error) => {
                console.error(error);
                setUnreachable(true);
            });

    useEffect(() => {
        setIsSuspended(stats?.isSuspended || server.status === 'suspended');
    }, [stats?.isSuspended, server.status]);

    useEffect(() => {
        // Don't waste a HTTP request if there is nothing important to show to the user because
        // the server is suspended.
        if (isSuspended || server.isNodeUnderMaintenance) return;

        getStats().then(() => {
            interval.current = setInterval(() => getStats(), 30000);
        });

        return () => {
            interval.current && clearInterval(interval.current);
        };
    }, [isSuspended, server.isNodeUnderMaintenance]);

    const limits = server.limits;
    const address = server.allocations
        .filter((alloc) => alloc.isDefault)
        .map((allocation) => `${allocation.alias || ip(allocation.ip)}:${allocation.port}`)
        .join(', ');

    let chip: { tone: Tone; text: string } | null = null;
    if (isSuspended) {
        chip = { tone: 'bad', text: server.status === 'suspended' ? 'Suspended' : 'Connection error' };
    } else if (server.isNodeUnderMaintenance) {
        chip = { tone: 'warn', text: 'Under maintenance' };
    } else if (server.isTransferring || server.status) {
        chip = {
            tone: 'neutral',
            text: server.isTransferring
                ? 'Transferring'
                : server.status === 'installing'
                ? 'Installing'
                : server.status === 'restoring_backup'
                ? 'Restoring backup'
                : 'Unavailable',
        };
    } else if (unreachable && !stats) {
        chip = { tone: 'warn', text: 'Unreachable' };
    } else if (stats) {
        chip =
            stats.status === 'running'
                ? { tone: 'good', text: 'Running' }
                : stats.status === 'offline'
                ? { tone: 'neutral', text: 'Offline' }
                : { tone: 'warn', text: stats.status === 'starting' ? 'Starting' : 'Stopping' };
    }

    const measured =
        !!stats && !isSuspended && !server.isNodeUnderMaintenance && !server.isTransferring && !server.status;

    return (
        <Link
            to={`/server/${server.id}`}
            className={className}
            css={tw`block rounded-xl bg-surface border border-line p-4 no-underline text-neutral-200 transition-colors duration-150 hover:border-neutral-600`}
        >
            <div css={tw`flex items-start justify-between`}>
                <div css={tw`flex items-center min-w-0`}>
                    <div
                        css={tw`w-10 h-10 mr-3 flex-shrink-0 flex items-center justify-center rounded-lg bg-neutral-700 text-neutral-300`}
                    >
                        <FontAwesomeIcon icon={faServer} />
                    </div>
                    <div css={tw`min-w-0`}>
                        <p css={tw`font-semibold text-neutral-50 truncate`}>{server.name}</p>
                        {!!server.description && (
                            <p css={tw`text-xs text-neutral-400 truncate`}>{server.description}</p>
                        )}
                    </div>
                </div>
                {chip && (
                    <div css={tw`ml-3`}>
                        <StatusChip tone={chip.tone}>{chip.text}</StatusChip>
                    </div>
                )}
            </div>
            <p css={tw`flex items-center mt-3 text-xs text-neutral-400`}>
                <FontAwesomeIcon icon={faEthernet} css={tw`mr-2 text-neutral-500`} />
                <span css={tw`font-mono`}>{address || 'No address'}</span>
            </p>
            <div css={tw`mt-4 grid grid-cols-3 gap-4`}>
                {measured && stats ? (
                    <>
                        <Meter
                            label={'CPU'}
                            value={`${stats.cpuUsagePercent.toFixed(1)}%`}
                            of={limits.cpu !== 0 ? `${limits.cpu}%` : 'unlimited'}
                            ratio={share(stats.cpuUsagePercent, limits.cpu)}
                        />
                        <Meter
                            label={'Memory'}
                            value={bytesToString(stats.memoryUsageInBytes)}
                            of={limits.memory !== 0 ? bytesToString(mbToBytes(limits.memory)) : 'unlimited'}
                            ratio={share(stats.memoryUsageInBytes, mbToBytes(limits.memory))}
                        />
                        <Meter
                            label={'Disk'}
                            value={bytesToString(stats.diskUsageInBytes)}
                            of={limits.disk !== 0 ? bytesToString(mbToBytes(limits.disk)) : 'unlimited'}
                            ratio={share(stats.diskUsageInBytes, mbToBytes(limits.disk))}
                        />
                    </>
                ) : !chip && !stats ? (
                    <div css={tw`col-span-3 flex justify-center`}>
                        <Spinner size={'small'} />
                    </div>
                ) : (
                    <p css={tw`col-span-3 text-xs text-neutral-500`}>Usage is not available right now.</p>
                )}
            </div>
        </Link>
    );
};
