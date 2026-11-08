import React from 'react';
import { faArrowLeft } from '@fortawesome/free-solid-svg-icons';
import tw from 'twin.macro';
import Can from '@/components/elements/Can';
import StatusChip, { Tone } from '@/components/elements/StatusChip';
import SidebarLink, { SidebarHeading } from '@/components/layout/SidebarLink';
import { ServerContext } from '@/state/server';
import routes from '@/routers/routes';

const states: Record<string, { tone: Tone; text: string }> = {
    running: { tone: 'good', text: 'Running' },
    starting: { tone: 'warn', text: 'Starting' },
    stopping: { tone: 'warn', text: 'Stopping' },
    offline: { tone: 'neutral', text: 'Offline' },
};

// The menu of a server: a way back, who it is, and the pages it has (the ones the person may see).
export default ({ to, loaded }: { to: (value: string, url?: boolean) => string; loaded: boolean }) => {
    const name = ServerContext.useStoreState((state) => state.server.data?.name);
    const status = ServerContext.useStoreState((state) => state.status.value);
    const chip = status ? states[status] : undefined;

    return (
        <>
            <SidebarLink to={'/'} exact icon={faArrowLeft}>
                All servers
            </SidebarLink>
            {name && (
                <div css={tw`mt-4 mb-2 px-3`}>
                    <p css={tw`font-semibold text-neutral-50 truncate`}>{name}</p>
                    {chip && (
                        <div css={tw`mt-1`}>
                            <StatusChip tone={chip.tone}>{chip.text}</StatusChip>
                        </div>
                    )}
                </div>
            )}
            {loaded && (
                <>
                    <SidebarHeading>Manage</SidebarHeading>
                    {routes.server
                        .filter((route) => !!route.name)
                        .map((route) =>
                            route.permission ? (
                                <Can key={route.path} action={route.permission} matchAny>
                                    <SidebarLink to={to(route.path, true)} exact={route.exact} icon={route.icon}>
                                        {route.name}
                                    </SidebarLink>
                                </Can>
                            ) : (
                                <SidebarLink
                                    key={route.path}
                                    to={to(route.path, true)}
                                    exact={route.exact}
                                    icon={route.icon}
                                >
                                    {route.name}
                                </SidebarLink>
                            )
                        )}
                </>
            )}
        </>
    );
};
