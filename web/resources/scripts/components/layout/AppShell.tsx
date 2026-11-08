import React, { useEffect, useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { useStoreState } from 'easy-peasy';
import tw from 'twin.macro';
import styled from 'styled-components/macro';
import { ApplicationStore } from '@/state';
import Logo from '@/components/elements/Logo';
import NavigationBar from '@/components/NavigationBar';

// The translate is written out by hand: twin.macro turns `-translate-x-full` into `transform: var(--tw-transform)`, which stays empty here.
const Drawer = styled.aside<{ $open: boolean }>`
    ${tw`fixed inset-y-0 left-0 z-30 w-64 flex flex-col bg-black border-r border-line transition-transform duration-200`};
    transform: translateX(${(props) => (props.$open ? '0' : '-100%')});

    @media (min-width: 1024px) {
        transform: none;
    }
`;

// The frame of every signed-in page: the menu down the left side (a drawer on a narrow screen), the thin bar on top, and the page.
const AppShell = ({ nav, children }: { nav: React.ReactNode; children: React.ReactNode }) => {
    const [open, setOpen] = useState(false);
    const { pathname } = useLocation();
    const name = useStoreState((state: ApplicationStore) => state.settings.data!.name);

    useEffect(() => {
        setOpen(false);
    }, [pathname]);

    return (
        <div>
            {open && (
                <div
                    css={tw`fixed inset-0 z-20 bg-black/60 lg:hidden`}
                    onClick={() => setOpen(false)}
                    aria-hidden={'true'}
                />
            )}
            <Drawer $open={open}>
                <Link
                    to={'/'}
                    css={tw`flex items-center h-14 px-5 flex-shrink-0 border-b border-line no-underline text-neutral-50`}
                >
                    <Logo size={22} css={tw`text-ember-500 mr-2`} />
                    <span css={tw`text-base font-semibold tracking-tight truncate`}>{name}</span>
                </Link>
                <nav css={tw`flex-1 overflow-y-auto px-3 py-4`}>{nav}</nav>
                <p css={tw`px-5 py-3 text-xs text-neutral-500 border-t border-line`}>Raptor Panel</p>
            </Drawer>
            <div css={tw`lg:pl-64 min-h-screen`}>
                <NavigationBar onMenu={() => setOpen(true)} />
                {children}
            </div>
        </div>
    );
};

export default AppShell;
