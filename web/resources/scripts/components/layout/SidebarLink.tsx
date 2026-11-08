import React from 'react';
import { NavLink } from 'react-router-dom';
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';
import { IconDefinition } from '@fortawesome/free-solid-svg-icons';
import styled from 'styled-components/macro';
import tw, { theme } from 'twin.macro';

const Link = styled(NavLink)`
    ${tw`flex items-center px-3 py-2 mb-0.5 rounded-lg text-sm text-neutral-300 no-underline transition-colors duration-150`};

    & > svg {
        ${tw`mr-3 text-neutral-500 transition-colors duration-150`};
        width: 1rem !important;
    }

    &:hover {
        ${tw`bg-surface text-neutral-100`};

        & > svg {
            ${tw`text-neutral-300`};
        }
    }

    &:focus-visible {
        outline-offset: -2px;
    }

    &.active {
        ${tw`bg-surface text-neutral-50`};
        box-shadow: inset 2px 0 0 ${theme`colors.ember.500`.toString()};

        & > svg {
            ${tw`text-ember-400`};
        }
    }
`;

type Props = { to: string; exact?: boolean; icon?: IconDefinition; children: React.ReactNode };

const SidebarLink = ({ to, exact, icon, children }: Props) => (
    <Link to={to} exact={exact}>
        {icon ? <FontAwesomeIcon icon={icon} fixedWidth /> : null}
        <span css={tw`truncate`}>{children}</span>
    </Link>
);

// A small heading above a group of links.
export const SidebarHeading = ({ children }: { children: React.ReactNode }) => (
    <p css={tw`px-3 mt-5 mb-1 text-xs font-medium text-neutral-500`}>{children}</p>
);

export default SidebarLink;
