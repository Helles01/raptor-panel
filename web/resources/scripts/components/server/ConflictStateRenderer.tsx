import React from 'react';
import { faBan, faCloudDownloadAlt, faTools, faWrench } from '@fortawesome/free-solid-svg-icons';
import { ServerContext } from '@/state/server';
import ScreenBlock from '@/components/elements/ScreenBlock';

export default () => {
    const status = ServerContext.useStoreState((state) => state.server.data?.status || null);
    const isTransferring = ServerContext.useStoreState((state) => state.server.data?.isTransferring || false);
    const isNodeUnderMaintenance = ServerContext.useStoreState(
        (state) => state.server.data?.isNodeUnderMaintenance || false
    );

    return status === 'installing' || status === 'install_failed' || status === 'reinstall_failed' ? (
        <ScreenBlock
            title={'Running installer'}
            icon={faWrench}
            message={'Your server should be ready soon, please try again in a few minutes.'}
        />
    ) : status === 'suspended' ? (
        <ScreenBlock
            title={'Server suspended'}
            icon={faBan}
            message={'This server is suspended and cannot be accessed.'}
        />
    ) : isNodeUnderMaintenance ? (
        <ScreenBlock
            title={'Node under maintenance'}
            icon={faTools}
            message={'The node of this server is currently under maintenance.'}
        />
    ) : (
        <ScreenBlock
            title={isTransferring ? 'Transferring' : 'Restoring from backup'}
            icon={faCloudDownloadAlt}
            message={
                isTransferring
                    ? 'Your server is being transferred to a new node, please check back later.'
                    : 'Your server is currently being restored from a backup, please check back in a few minutes.'
            }
        />
    );
};
