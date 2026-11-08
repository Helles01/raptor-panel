import * as React from 'react';
import { useState } from 'react';
import { Link, NavLink } from 'react-router-dom';
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';
import { faBars, faSignOutAlt } from '@fortawesome/free-solid-svg-icons';
import { useStoreState } from 'easy-peasy';
import { ApplicationStore } from '@/state';
import SearchContainer from '@/components/dashboard/search/SearchContainer';
import tw from 'twin.macro';
import styled from 'styled-components/macro';
import http from '@/api/http';
import SpinnerOverlay from '@/components/elements/SpinnerOverlay';
import Tooltip from '@/components/elements/tooltip/Tooltip';
import Avatar from '@/components/Avatar';
import Logo from '@/components/elements/Logo';

const Actions = styled.div`
    & > a,
    & > button,
    & > .navigation-link {
        ${tw`flex items-center justify-center h-9 w-9 ml-1 rounded-lg no-underline text-neutral-400 cursor-pointer transition-colors duration-150`};

        &:active,
        &:hover {
            ${tw`text-neutral-100 bg-surface`};
        }
    }
`;

// The thin bar on top of a page: the menu button on a narrow screen, and search, the account and signing out.
export default ({ onMenu }: { onMenu: () => void }) => {
    const name = useStoreState((state: ApplicationStore) => state.settings.data!.name);
    const [isLoggingOut, setIsLoggingOut] = useState(false);

    const onTriggerLogout = () => {
        setIsLoggingOut(true);
        http.post('/auth/logout').finally(() => {
            // @ts-expect-error this is valid
            window.location = '/';
        });
    };

    return (
        <header css={tw`sticky top-0 z-10 flex items-center h-14 px-4 bg-canvas border-b border-line`}>
            <SpinnerOverlay visible={isLoggingOut} />
            <button
                css={tw`lg:hidden flex items-center justify-center h-9 w-9 mr-2 rounded-lg text-neutral-300 hover:bg-surface`}
                onClick={onMenu}
                aria-label={'Open the menu'}
            >
                <FontAwesomeIcon icon={faBars} />
            </button>
            <Link to={'/'} css={tw`lg:hidden flex items-center no-underline text-neutral-50`}>
                <Logo size={20} css={tw`text-ember-500 mr-2`} />
                <span css={tw`font-semibold tracking-tight`}>{name}</span>
            </Link>
            <div css={tw`flex-1`} />
            <Actions css={tw`flex items-center`}>
                <SearchContainer />
                <Tooltip placement={'bottom'} content={'Account settings'}>
                    <NavLink to={'/account'}>
                        <span css={tw`flex items-center w-5 h-5`}>
                            <Avatar.User />
                        </span>
                    </NavLink>
                </Tooltip>
                <Tooltip placement={'bottom'} content={'Sign out'}>
                    <button onClick={onTriggerLogout}>
                        <FontAwesomeIcon icon={faSignOutAlt} />
                    </button>
                </Tooltip>
            </Actions>
        </header>
    );
};
