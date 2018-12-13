// Libraries
import React, {SFC} from 'react'
import {Link} from 'react-router'
import classnames from 'classnames'
import _ from 'lodash'

// Components
import NavMenuSubItem from 'src/pageLayout/components/NavMenuSubItem'

// Types
import {IconFont} from 'src/clockface'

interface Props {
  icon: IconFont
  title: string
  link: string
  children?: JSX.Element | JSX.Element[]
  location: string
  highlightWhen: string[]
}

const NavMenuItem: SFC<Props> = ({
  icon,
  title,
  link,
  children,
  location,
  highlightWhen,
}) => {
  const {length} = _.intersection(_.split(location, '/'), highlightWhen)
  const isActive = !!length

  return (
    <div className={classnames('nav--item', {active: isActive})}>
      <Link className="nav--item-icon" to={link}>
        <span className={`icon sidebar--icon ${icon}`} />
      </Link>
      <div className="nav--item-menu">
        <Link className="nav--item-header" to={link}>
          {title}
        </Link>
        {React.Children.map(
          children,
          (child: JSX.Element) => (child.type === NavMenuSubItem ? child : null)
        )}
      </div>
    </div>
  )
}

export default NavMenuItem
